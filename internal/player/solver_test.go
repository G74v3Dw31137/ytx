package player

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func fixture(t testing.TB) string {
	t.Helper()
	b, e := os.ReadFile("../../testdata/resolve-player.js")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func compiled(t testing.TB, source string) *goja.Program {
	t.Helper()
	p, e := Compile(context.Background(), []byte(source))
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestDiscovery(t *testing.T) {
	src := fixture(t)
	for _, tc := range []struct {
		name, src string
		want      error
		init      bool
	}{
		{"valid", src, nil, true},
		{"extra-method-decoy", strings.Replace(src, "{toString:", `{noop:function(){return "ignored"},toString:`, 1), ErrAmbiguous, true},
		{"noncallable", strings.Replace(src, "{toString:function(){", `{extra:42,toString:function(){`, 1), ErrAmbiguous, true},
		{"only-noncallable", strings.Replace(src, "toString:function(){", `toString:42,unused:function(){`, 1), ErrAmbiguous, true},
		{"multiple-factories", strings.Replace(src, ";})(_yt_player);", `;other=function(a,b,c){var x={};x.set("alr","yes");return x;};})(_yt_player);`, 1), ErrAmbiguous, false},
		{"no-factory", strings.Replace(src, `"alr"`, `"other"`, 1), ErrUnsupported, false},
		{"bad-return", strings.Replace(src, "return u;", "return 1;", 1), ErrUnsupported, false},
		{"unsupported-wrapper", "(function(){})();", ErrUnsupported, false},
		{"malformed", "var (", ErrUnsupported, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := Compile(context.Background(), []byte(tc.src))
			if e == nil && tc.init {
				_, e = NewSession(context.Background(), p)
			}
			if !errors.Is(e, tc.want) {
				t.Fatalf("error=%v want %v", e, tc.want)
			}
		})
	}
}
func TestPrimitiveResultsAndSafeErrors(t *testing.T) {
	src := fixture(t)
	for _, expression := range []string{`null`, `undefined`, `""`, `123`, `{secret:"PRIVATE"}`, `new String("boxed")`, `"enhanced_except_PRIVATE"`, `"x".repeat(16385)`} {
		t.Run(expression, func(t *testing.T) {
			s, e := NewSession(context.Background(), compiled(t, strings.Replace(src, `"ok_"+this.values.n`, expression, 1)))
			if e != nil {
				t.Fatal(e)
			}
			out, e := s.N(context.Background(), "input")
			if out != "" || !errors.Is(e, ErrExecution) || strings.Contains(e.Error(), "PRIVATE") {
				t.Fatal("invalid result policy")
			}
		})
	}
	s, e := NewSession(context.Background(), compiled(t, src))
	if e != nil {
		t.Fatal(e)
	}
	for _, input := range []string{"", strings.Repeat("x", MaxValue+1), "throw"} {
		if v, e := s.N(context.Background(), input); v != "" || !errors.Is(e, ErrExecution) || strings.Contains(e.Error(), "SECRET") {
			t.Fatal("unsafe failure")
		}
	}
	if n, e := s.N(context.Background(), "normal"); e != nil || n != "ok_normal" {
		t.Fatal(e)
	}
}
func TestFreshRuntimeState(t *testing.T) {
	src := strings.Replace(fixture(t), "var constants=", "var calls=0;var constants=", 1)
	src = strings.Replace(src, `"ok_"+this.values.n`, `"ok_"+(++calls)+"_"+this.values.n`, 1)
	p := compiled(t, src)
	for range 3 {
		s, e := NewSession(context.Background(), p)
		if e != nil {
			t.Fatal(e)
		}
		v, e := s.N(context.Background(), "same")
		if e != nil || v != "ok_1_same" {
			t.Fatal("state crossed session")
		}
	}
}
func TestCancellationAndJoinedInterrupt(t *testing.T) {
	p := compiled(t, fixture(t))
	s, e := NewSession(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.N(ctx, "anything"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e = s.N(ctx, "hang"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	// Reusing this test-owned VM verifies that the joined callback is disarmed.
	if v, e := s.N(context.Background(), "after"); e != nil || v != "ok_after" {
		t.Fatal(e)
	}
	loop, err := goja.Compile("init-loop", `while(true){}`, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e = NewSession(ctx, loop); !errors.Is(e, ErrInitialization) || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}

// Historical assets stay outside the distributable source tree. Missing archives
// are explicit skips; the P3 acceptance run requires both and records their hashes.
var historicalPaths = []string{"YTX_PLAYER_ORIGINAL_GZIP", "YTX_PLAYER_REDACTED_GZIP"}

func historical(t testing.TB, path string) []byte {
	t.Helper()
	path = os.Getenv(path)
	if path == "" {
		t.Skip("external historical archive environment variable unset")
	}
	f, e := os.Open(path)
	if errors.Is(e, os.ErrNotExist) {
		t.Skip("external historical archive unavailable")
	}
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	z, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	b, e := io.ReadAll(z)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestHistoricalIndependentReferences(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/d5-reference.json")
	if e != nil {
		t.Fatal(e)
	}
	var rows []struct{ Kind, ID, Mode, Input, Status, Output string }
	if e = json.Unmarshal(raw, &rows); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 46 {
		t.Fatal("reference count", len(rows))
	}
	for _, path := range historicalPaths {
		t.Run(path, func(t *testing.T) {
			source := historical(t, path)
			sum := sha256.Sum256(source)
			p := compiled(t, string(source))
			good, rejected := 0, 0
			for _, r := range rows {
				s, e := NewSession(context.Background(), p)
				if e != nil {
					t.Fatal(e)
				}
				var out string
				if r.Kind == "signature" {
					out, e = s.Signature(context.Background(), r.Input)
				} else {
					out, e = s.N(context.Background(), r.Input)
				}
				if r.Status == "string" {
					if e != nil || out != r.Output {
						t.Fatalf("reference %s mismatch; error=%v", r.ID, e)
					}
					good++
				} else {
					if e == nil || out != "" {
						t.Fatalf("null reference %s accepted", r.ID)
					}
					rejected++
				}
			}
			if good != 40 || rejected != 6 {
				t.Fatal(good, rejected)
			}
			t.Logf("sha256=%s exact=%d null-rejections=%d", hex.EncodeToString(sum[:]), good, rejected)
		})
	}
}
func TestCapturedHistoricalReplay(t *testing.T) {
	path := os.Getenv("YTX_PLAYER_CAPTURE_JSON")
	if path == "" {
		t.Skip("external capture environment variable unset")
	}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		t.Skip("private capture unavailable")
	}
	if e != nil {
		t.Fatal(e)
	}
	type observation struct {
		Input, Output string
		Success       bool
	}
	var capture struct {
		Asset struct {
			Source []byte
			SHA256 string
		}
		Signature, N observation
	}
	if e = json.Unmarshal(b, &capture); e != nil {
		t.Fatal(e)
	}
	if !capture.Signature.Success || !capture.N.Success {
		t.Fatal("historical observation unavailable")
	}
	s, e := NewSession(context.Background(), compiled(t, string(capture.Asset.Source)))
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []struct {
		kind string
		o    observation
	}{{"s", capture.Signature}, {"n", capture.N}} {
		var got string
		if r.kind == "s" {
			got, e = s.Signature(context.Background(), r.o.Input)
		} else {
			got, e = s.N(context.Background(), r.o.Input)
		}
		if e != nil || got != r.o.Output {
			t.Fatal("private historical replay mismatch")
		}
	}
	t.Log("both captured outputs exact; supplementary same-engine evidence")
}

func TestIdentityNAndStackBound(t *testing.T) {
	src := fixture(t)
	s, e := NewSession(context.Background(), compiled(t, strings.Replace(src, `"ok_"+this.values.n`, `this.values.n`, 1)))
	if e != nil {
		t.Fatal(e)
	}
	if v, e := s.N(context.Background(), "same"); e != nil || v != "same" {
		t.Fatal("identity transform rejected")
	}
	s, e = NewSession(context.Background(), compiled(t, strings.Replace(src, `"ok_"+this.values.n`, `(function recurse(){return recurse()})()`, 1)))
	if e != nil {
		t.Fatal(e)
	}
	if v, e := s.N(context.Background(), "same"); v != "" || !errors.Is(e, ErrExecution) {
		t.Fatal("unbounded recursion accepted")
	}
}
func TestFiniteExecutionDeadline(t *testing.T) {
	s, e := NewSession(context.Background(), compiled(t, fixture(t)))
	if e != nil {
		t.Fatal(e)
	}
	if out, e := s.N(context.Background(), "hang"); out != "" || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestSignaturePrimitiveBeforeDecoding(t *testing.T) {
	src := fixture(t)
	for _, expr := range []string{`null`, `undefined`, `42`, `new String("boxed")`, `({toString:function(){throw Error("PRIVATE")}})`} {
		changed := strings.Replace(src, `this.values.s=encodeURIComponent(decodeURIComponent(this.values.s).split("").reverse().join(""))`, `this.values.s=`+expr, 1)
		s, e := NewSession(context.Background(), compiled(t, changed))
		if e != nil {
			t.Fatal(e)
		}
		if out, e := s.Signature(context.Background(), "abc"); out != "" || !errors.Is(e, ErrExecution) {
			t.Fatal("nonprimitive signature accepted")
		}
	}
}

func TestInitializationAccessorsAreBounded(t *testing.T) {
	for _, body := range []string{`throw Error("PRIVATE")`, `while(true){}`} {
		p, e := goja.Compile("accessor.js", `Object.defineProperty(globalThis,"__extractMethodSource",{get:function(){`+body+`}})`, false)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, e = NewSession(ctx, p)
		cancel()
		if !errors.Is(e, ErrInitialization) || strings.Contains(e.Error(), "PRIVATE") {
			t.Fatal("unsafe initialization accessor")
		}
		if strings.HasPrefix(body, "while") && !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal("unbounded accessor")
		}
	}
}
func TestUnsupportedMethodShapes(t *testing.T) {
	for _, src := range []string{
		`function(){return 42}`, `function(){return this.value}`, `function(){return this.get("n")}`, `function(x){return this.value}`, `async function(){return this.value}`, `function*(){return this.value}`, `function(){this.value=42}`, `()=>this.value`,
	} {
		if validMethodShape(src) {
			t.Fatal("unsupported method accepted")
		}
	}
	for _, src := range []string{`function(){this.set("n", "value");return "url"}`, `renamed(){this.x=1;return this.x}`} {
		if !validMethodShape(src) {
			t.Fatal("supported method rejected")
		}
	}
}

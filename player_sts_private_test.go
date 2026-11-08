package ytx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/dop251/goja/parser"
)

const p23Method = `class C { #if() {} m() { this.#if() / "x/;x={sts:11111};/x" / 1; } };`
const p23Field = `class C { #return = 1; m() { this.#return / "x/;x={sts:11111};/x" / 1; } };`

// The cached parser validates authored synthetic syntax only; no JS is executed.
func p23Syntax(t *testing.T, source string) {
	t.Helper()
	if _, err := parser.ParseFile(nil, "synthetic.js", source, 0); err != nil {
		t.Fatalf("invalid test source: %v\n%s", err, source)
	}
}

func TestP23PrivateIdentifiers(t *testing.T) {
	sources := []string{
		p23Method, p23Field,
		`class C { #if(){} m(){ this.#if() / /\{sts:11111\}/.source.length; } };`,
		`class C { #while=1; m(){ if(this.#while) /\{sts:11111\}/.test(''); } };`,
		`class C { #return=1; m(){ return (this.#return * 2) / 1; } };`,
	}
	for _, name := range []string{"if", "return", "while", "for", "switch", "catch", "throw", "class", "sts", "signatureTimestamp", "é", "a\u200c"} {
		for _, expr := range []string{"this.#%s", "obj.#%s", "(this.#%s)", "this.#%s + 2"} {
			sources = append(sources, fmt.Sprintf(`class C { #%s=1; m(obj) { %s / "x/;x={sts:11111};/x" / 1; } };`, name, fmt.Sprintf(expr, name)))
		}
		for _, expr := range []string{"this.#%s()", "(this.#%s())", "this.#%s(this.#%s())"} {
			sources = append(sources, fmt.Sprintf(`class C { #%s(){} m() { %s / "x/;x={sts:11111};/x" / 1; } };`, name, strings.ReplaceAll(expr, "%s", name)))
		}
	}
	for i, source := range sources {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			p23Syntax(t, source)
			for _, suffix := range []string{"", `x={sts:20760};`, `x={sts:20760};y={signatureTimestamp:20760};`, `x={sts:20760};y={sts:20761};`} {
				n, err := extractSTS([]byte(source + suffix))
				wantError := suffix == "" || strings.Contains(suffix, "20761")
				if wantError {
					if !errors.Is(err, ErrPlayerScript) {
						t.Fatalf("STS=%d error=%v", n, err)
					}
				} else if err != nil || n != 20760 {
					t.Fatalf("STS=%d error=%v", n, err)
				}
			}
		})
	}
	// Names are indivisible and cannot become candidate keys or control tokens.
	l := stsLexer{source: []byte(`#if #return #sts #signatureTimestamp`), expression: true}
	for _, want := range []string{"#if", "#return", "#sts", "#signatureTimestamp"} {
		if got, err := l.next(0); err != nil || got != want || l.expression {
			t.Fatalf("token=%q expression=%t error=%v", got, l.expression, err)
		}
	}
}

func TestP23PrivateOpacityAndUnicode(t *testing.T) {
	for _, literal := range []string{
		`"x/;x={sts:11111};/x"`, `'x/;x={sts:11111};/x'`,
		"`#if ${this.#if() / 'x/;x={sts:11111};/x' / 1} {sts:11111}`",
		`/\{sts:11111\}/`, `/[{}#;sts:11111/]/`,
	} {
		source := `class C { #if(){} m(){ this.#if(); var x=` + literal + `; } };`
		p23Syntax(t, source)
		if _, err := extractSTS([]byte(source)); !errors.Is(err, ErrPlayerScript) {
			t.Fatal("literal leaked", err)
		}
		if n, err := extractSTS([]byte(source + `x={sts:20760};`)); err != nil || n != 20760 {
			t.Fatalf("%d %v", n, err)
		}
	}
	for _, space := range []string{"\u2028", "\u2029", "\r", "\n", "\r\n", "\u00a0", "\ufeff", "\u2003"} {
		for _, comment := range []string{"// #return {sts:11111}" + space, "/* #if {sts:11111} */" + space} {
			// Unicode spaces alone do not terminate line comments; add LF explicitly.
			source := `class C { #while=1; m(){ this.#while; ` + comment + "\n /x/" + space + `;if(1)/,sts:11111,/.test(''); } };`
			p23Syntax(t, source)
			if n, err := extractSTS([]byte(source + `x={sts:20760};`)); err != nil || n != 20760 {
				t.Fatalf("space %q: %d %v", space, n, err)
			}
			if _, err := extractSTS([]byte(source)); err == nil {
				t.Fatal("comment/regex leaked")
			}
		}
	}
	for _, source := range []string{`var \u0061=1;`, `var \u{61}=1;`} {
		if n, e := extractSTS([]byte(p23Method + source + `x={sts:20760};`)); e != nil || n != 20760 {
			t.Fatalf("escaped ordinary identifier: %d %v", n, e)
		}
	}
	for _, bad := range [][]byte{{0xff}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}} {
		if _, err := extractSTS(append([]byte(p23Method+`x={sts:20760};//`), bad...)); err == nil {
			t.Fatal("invalid UTF-8 accepted")
		}
	}
}

func TestP23PrivateMalformedAndUnsupported(t *testing.T) {
	// Intentionally malformed/truncated sources, separate from syntax-oracle cases.
	for _, suffix := range []string{`#`, `this.#`, `this.#;`, `#1`, `# if`, `#\u0069f`, `#i\u0066`, `#\u{69}f`, `([)]`, `(`, `]`, `"unfinished`, `'unfinished`, `/unfinished`, "`unfinished", "`${this.#if(}`", `class C { #if(`} {
		if _, err := extractSTS([]byte(p23Method + `x={sts:20760};` + suffix)); !errors.Is(err, ErrPlayerScript) {
			t.Fatalf("accepted %q: %v", suffix, err)
		}
	}
	// Escaped private names are valid JS, but explicitly outside the lexer subset.
	source := `class C { #\u0069f(){} m(){ this.#\u0069f(); } };x={sts:20760};`
	p23Syntax(t, source)
	if _, err := extractSTS([]byte(source)); !errors.Is(err, ErrPlayerScript) {
		t.Fatal("escaped private name accepted")
	}
}

func p23Counts(t *testing.T, f *p2Transport, wantGET, wantPOST int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	gets, posts := 0, 0
	for i, r := range f.requests {
		switch r.Method {
		case "GET":
			gets++
		case "POST":
			posts++
			var body struct {
				PlaybackContext struct {
					ContentPlaybackContext struct {
						STS int `json:"signatureTimestamp"`
					}
				}
			}
			if json.Unmarshal(f.bodies[i], &body) != nil || body.PlaybackContext.ContentPlaybackContext.STS != 20760 {
				t.Fatalf("wrong Player STS: %s", f.bodies[i])
			}
		default:
			t.Fatal("unexpected request")
		}
	}
	if gets != wantGET || posts != wantPOST {
		t.Fatalf("GET/POST=%d/%d want %d/%d", gets, posts, wantGET, wantPOST)
	}
	t.Logf("JS GET=%d Player POST=%d; every POST STS=20760", gets, posts)
}

func TestP23PrivatePlayerRecovery(t *testing.T) {
	for _, source := range []string{p23Method, p23Field} {
		f := newP2Transport(t)
		f.script = []byte(source)
		c := newP2Client(t, f)
		if v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa"); v != nil || !errors.Is(err, ErrPlayerScript) {
			t.Fatalf("fake-only result: %v %v", v, err)
		}
		c.mu.Lock()
		cached := len(c.assets)
		c.mu.Unlock()
		if cached != 0 {
			t.Fatal("failed extraction cached")
		}
		p23Counts(t, f, 1, 0)
		f.mu.Lock()
		f.script = append(f.script, []byte(`x={sts:20760};`)...)
		f.mu.Unlock()
		for range 3 {
			v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
			if err != nil || v.player.sts != 20760 {
				t.Fatalf("recovery: %v", err)
			}
		}
		p23Counts(t, f, 2, 3)
		f.base.checkCalls(t, 1, 1, 1, 0)
	}
}

func TestP23PrivateConcurrentPlayer(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		t.Run(fmt.Sprint(healthy), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newP2Transport(t)
				f.script = []byte(p23Method)
				if healthy {
					f.script = append(f.script, []byte(`x={sts:20760};`)...)
				}
				release := make(chan struct{})
				f.hook = func(r *http.Request) (*http.Response, error) {
					if r.Method == "GET" {
						<-release
						return f.reply(r, 200, f.script), nil
					}
					return f.reply(r, 200, f.response), nil
				}
				c := newP2Client(t, f)
				done := make(chan error, 8)
				for range 8 {
					go func() {
						v, e := c.GetVideo(context.Background(), "aaaaaaaaaaa")
						if e == nil && (v == nil || v.player.sts != 20760 || !bytes.Contains(v.player.source, []byte(`sts:20760`))) {
							e = fmt.Errorf("fabricated asset")
						}
						done <- e
					}()
				}
				synctest.Wait()
				close(release)
				for range 8 {
					err := <-done
					if healthy && err != nil || !healthy && !errors.Is(err, ErrPlayerScript) {
						t.Fatalf("healthy=%t err=%v", healthy, err)
					}
				}
				synctest.Wait()
				posts := 0
				if healthy {
					posts = 8
				}
				p23Counts(t, f, 1, posts)
				if !healthy && len(c.assets) != 0 {
					t.Fatal("fabricated cache entry")
				}
				f.base.checkCalls(t, 1, 1, 1, 0)
			})
		})
	}
}

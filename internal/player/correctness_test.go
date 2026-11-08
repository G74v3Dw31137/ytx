package player

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dop251/goja"
	"os"
	"strings"
	"testing"
)

func TestInitializationExpressionsMatchOriginal(t *testing.T) {
	for _, init := range []string{`revision++;`, `revision+=1;`, `revision=1,revision++;`, `(function(){revision++})();`} {
		src := strings.Replace(fixture(t), "var constants=", "var revision=0;"+init+"var constants=", 1)
		src = strings.Replace(src, `"ok_"+this.values.n`, `"ok_"+revision+"_"+this.values.n`, 1)
		// Export the original closure's factory without running preparation.
		original := strings.Replace(src, `;})(_yt_player);`, `;globalThis.originalFactory=factory;})(_yt_player);`, 1)
		vm := goja.New()
		if _, e := vm.RunString(original); e != nil {
			t.Fatal(e)
		}
		want, e := vm.RunString(`var u=originalFactory("url","s",undefined);u.set("n","input");u.toString();u.get("n")`)
		if e != nil {
			t.Fatal(e)
		}
		s, e := NewSession(context.Background(), compiled(t, src))
		if e != nil {
			t.Fatal(e)
		}
		got, e := s.N(context.Background(), "input")
		if e != nil || got != want.String() || got == "ok_0_input" {
			t.Fatalf("original=%s prepared=%s error=%v", want, got, e)
		}
	}
}

func TestUnsupportedStartupFailsExplicitly(t *testing.T) {
	src := strings.Replace(fixture(t), "var constants=", `window.addEventListener("load",function(){});var constants=`, 1)
	if _, e := Prepare([]byte(src)); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	src = strings.Replace(fixture(t), "var constants=", `(function(){throw Error("startup")})();var constants=`, 1)
	if _, e := NewSession(context.Background(), compiled(t, src)); !errors.Is(e, ErrInitialization) {
		t.Fatal(e)
	}
}

func TestReadOnlyMethodRejected(t *testing.T) {
	src := fixture(t)
	start := strings.Index(src, "  if(this.values.s")
	end := strings.Index(src, `  return "url";`)
	src = src[:start] + `return this.values.n;` + src[end+len(`  return "url";`):]
	if _, e := NewSession(context.Background(), compiled(t, src)); !errors.Is(e, ErrUnsupported) {
		t.Fatal("read-only decoy accepted", e)
	}
}

func TestHistoricalCombinedSignatureThenN(t *testing.T) {
	b, e := os.ReadFile("../../testdata/d5-reference.json")
	if e != nil {
		t.Fatal(e)
	}
	var rows []struct{ Kind, Status, Input, Output string }
	if e = json.Unmarshal(b, &rows); e != nil {
		t.Fatal(e)
	}
	var sig, n struct{ Input, Output string }
	for _, r := range rows {
		if r.Status != "string" {
			continue
		}
		if r.Kind == "signature" && sig.Input == "" {
			sig.Input, sig.Output = r.Input, r.Output
		}
		if r.Kind == "n" && n.Input == "" {
			n.Input, n.Output = r.Input, r.Output
		}
	}
	if sig.Input == "" || n.Input == "" {
		t.Fatal("missing independent vectors")
	}
	for _, path := range historicalPaths {
		t.Run(path, func(t *testing.T) {
			p := compiled(t, string(historical(t, path)))
			for range 2 {
				s, e := NewSession(context.Background(), p)
				if e != nil {
					t.Fatal(e)
				}
				if got, e := s.Signature(context.Background(), sig.Input); e != nil || got != sig.Output {
					t.Fatal("signature reference mismatch", e)
				}
				if got, e := s.N(context.Background(), n.Input); e != nil || got != n.Output {
					t.Fatal("n reference mismatch", e)
				}
			}
		})
	}
}

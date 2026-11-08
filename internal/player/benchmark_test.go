package player

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func BenchmarkPlayer(b *testing.B) {
	source := historical(b, historicalPaths[1])
	code, e := Prepare(source)
	if e != nil {
		b.Fatal(e)
	}
	p, e := CompileCode(code)
	if e != nil {
		b.Fatal(e)
	}
	raw, e := os.ReadFile("../../testdata/d5-reference.json")
	if e != nil {
		b.Fatal(e)
	}
	var rows []struct{ Kind, Status, Input string }
	if e = json.Unmarshal(raw, &rows); e != nil {
		b.Fatal(e)
	}
	var sig, n string
	for _, r := range rows {
		if r.Status == "string" {
			if r.Kind == "signature" && sig == "" {
				sig = r.Input
			}
			if r.Kind == "n" && n == "" {
				n = r.Input
			}
		}
	}
	if sig == "" || n == "" {
		b.Fatal("missing benchmark inputs")
	}
	b.Run("ASTPreparation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, e := Prepare(source); e != nil {
				b.Fatal(e)
			}
		}
	})
	b.Run("Compilation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, e := CompileCode(code); e != nil {
				b.Fatal(e)
			}
		}
	})
	b.Run("FreshRuntimeInitialization", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, e := NewSession(context.Background(), p); e != nil {
				b.Fatal(e)
			}
		}
	})
	for _, kind := range []string{"Signature", "N"} {
		b.Run(kind, func(b *testing.B) {
			s, e := NewSession(context.Background(), p)
			if e != nil {
				b.Fatal(e)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if kind == "Signature" {
					_, e = s.Signature(context.Background(), sig)
				} else {
					_, e = s.N(context.Background(), n)
				}
				if e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

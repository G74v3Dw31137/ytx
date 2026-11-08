package ytx

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func BenchmarkResolve(b *testing.B) {
	path := os.Getenv("YTX_PLAYER_REDACTED_GZIP")
	if path == "" {
		b.Skip("external historical archive environment variable unset")
	}
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		b.Skip("external historical archive unavailable")
	}
	if e != nil {
		b.Fatal(e)
	}
	defer f.Close()
	z, e := gzip.NewReader(f)
	if e != nil {
		b.Fatal(e)
	}
	defer z.Close()
	source, e := io.ReadAll(z)
	if e != nil {
		b.Fatal(e)
	}
	raw, e := os.ReadFile("testdata/d5-reference.json")
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
	c := resolveClient(b)
	v := resolveVideo(b, c, resolveAsset(b, string(source)), cipherFormat(mediaBase+"?n="+n, sig, ""))
	format := v.Formats()[0]
	if _, e = c.Resolve(context.Background(), v, format); e != nil {
		b.Fatal(e)
	}
	b.Run("CachedProgram", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, e := c.Resolve(context.Background(), v, format); e != nil {
				b.Fatal(e)
			}
		}
	})
	b.Run("Concurrent", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, e := c.Resolve(context.Background(), v, format); e != nil {
					b.Error(e)
				}
			}
		})
	})
}

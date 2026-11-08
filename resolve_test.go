package ytx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/G74v3Dw31137/potter"
)

const mediaBase = "https://rr1---sn-test.googlevideo.com/videoplayback"

func resolveClient(t testing.TB) *Client {
	t.Helper()
	c, e := NewClient(WithHTTPClient(&http.Client{Transport: metadataTransport(func(*http.Request) (*http.Response, error) {
		t.Error("Resolve attempted HTTP")
		return nil, errors.New("forbidden")
	})}))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func resolveAsset(t testing.TB, source string) playerAsset {
	t.Helper()
	if source == "" {
		b, e := os.ReadFile("testdata/resolve-player.js")
		if e != nil {
			t.Fatal(e)
		}
		source = string(b)
	}
	sum := sha256.Sum256([]byte(source))
	return playerAsset{url: "https://www.youtube.com/s/player/fixture/base.js", source: []byte(source), sha256: hex.EncodeToString(sum[:]), sts: 20760}
}
func resolveVideo(t testing.TB, c *Client, asset playerAsset, formats ...Format) *Video {
	t.Helper()
	response := map[string]any{"playabilityStatus": map[string]string{"status": "OK"}, "streamingData": map[string]any{"formats": formats}}
	raw, e := json.Marshal(response)
	if e != nil {
		t.Fatal(e)
	}
	v, e := parseVideo(raw, SessionSnapshot{tokens: potter.Tokens{GVS: "origin+GVS/=", Player: "DIFFERENT-PLAYER"}}, asset, "video")
	if e != nil {
		t.Fatal(e)
	}
	v.owner = c.identity
	return v
}
func resolveOne(t testing.TB, c *Client, f Format) (string, error) {
	t.Helper()
	v := resolveVideo(t, c, resolveAsset(t, ""), f)
	return c.Resolve(context.Background(), v, v.Formats()[0])
}
func cipherFormat(raw, s, sp string) Format {
	q := url.Values{"url": {raw}, "s": {s}}
	if sp != "" {
		q.Set("sp", sp)
	}
	return Format{Itag: 1, SignatureCipher: q.Encode()}
}
func TestResolveOwnership(t *testing.T) {
	c := resolveClient(t)
	other := resolveClient(t)
	a := resolveAsset(t, "")
	f := Format{Itag: 7, URL: mediaBase, AudioTrack: &AudioTrack{ID: "one"}, InitRange: &ByteRange{Start: "0"}, IndexRange: &ByteRange{End: "2"}, MIMEType: `audio/mp4; codecs="mp4a.40.2"`}
	v := resolveVideo(t, c, a, f, f)
	second := resolveVideo(t, c, a, f)
	formats := v.Formats()
	if formats[0].ordinal == formats[1].ordinal {
		t.Fatal("duplicate descriptors lost identity")
	}
	for _, tc := range []struct {
		name   string
		client *Client
		video  *Video
		format Format
		want   error
	}{
		{"valid", c, v, formats[0], nil}, {"duplicate", c, v, formats[1], nil},
		{"foreign-client", other, v, formats[0], ErrOwnership}, {"foreign-video", c, second, formats[0], ErrOwnership},
		{"nil-video", c, nil, formats[0], ErrOwnership}, {"zero", c, v, Format{}, ErrOwnership}, {"reconstructed", c, v, f, ErrOwnership},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, e := tc.client.Resolve(context.Background(), tc.video, tc.format)
			if !errors.Is(e, tc.want) {
				t.Fatalf("error=%v", e)
			}
			if e != nil && out != "" {
				t.Fatal("partial URL")
			}
		})
	}
	raw, _ := json.Marshal(formats[0])
	var decoded Format
	_ = json.Unmarshal(raw, &decoded)
	if _, e := c.Resolve(context.Background(), v, decoded); !errors.Is(e, ErrOwnership) {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Format){func(f *Format) { f.Itag++ }, func(f *Format) { f.Codecs[0] = "modified" }, func(f *Format) { f.AudioTrack.ID = "modified" }, func(f *Format) { f.InitRange.Start = "modified" }, func(f *Format) { f.IndexRange.End = "modified" }, func(f *Format) { f.Adaptive = !f.Adaptive }, func(f *Format) { f.URL += "?changed=1" }} {
		copy := v.Formats()[0]
		mutate(&copy)
		if out, e := c.Resolve(context.Background(), v, copy); !errors.Is(e, ErrFormatMutation) || out != "" {
			t.Fatal("mutation accepted")
		}
	}
	formats[0], formats[1] = formats[1], formats[0]
	for _, f := range formats {
		if _, e := c.Resolve(context.Background(), v, f); e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			if _, e := c.Resolve(context.Background(), v, formats[0]); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	c.Close()
	if _, e := c.Resolve(context.Background(), v, formats[0]); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
}
func TestOwnershipBothArrays(t *testing.T) {
	c := resolveClient(t)
	raw := []byte(`{"playabilityStatus":{"status":"OK"},"streamingData":{"formats":[{"itag":7,"url":"` + mediaBase + `"},{"itag":7,"url":"` + mediaBase + `"}],"adaptiveFormats":[{"itag":7,"url":"` + mediaBase + `"}]}}`)
	v, e := parseVideo(raw, SessionSnapshot{tokens: potter.Tokens{GVS: "proof"}}, resolveAsset(t, ""), "video")
	if e != nil {
		t.Fatal(e)
	}
	v.owner = c.identity
	for i, f := range v.Formats() {
		if f.ordinal != i || f.Adaptive != (i == 2) {
			t.Fatal("ordering")
		}
		if _, e := c.Resolve(context.Background(), v, f); e != nil {
			t.Fatal(e)
		}
	}
}
func TestResolveRawQueryAndTransforms(t *testing.T) {
	c := resolveClient(t)
	raw := strings.Replace(mediaBase, "videoplayback", "%76ideoplayback", 1) + "?a=%2f&dup=1&flag&dup=two+words&x=%20&n=abc&v=untouched&sparams=a%2Cdup&lsparams=x&lsig=keep%2f&tail="
	f := cipherFormat(raw, "abc", "custom_sig")
	got, e := resolveOne(t, c, f)
	if e != nil {
		t.Fatal(e)
	}
	want := strings.Replace(raw, "n=abc", "n=ok_abc", 1) + "&custom_sig=cba&pot=origin%2BGVS%2F%3D"
	if got != want {
		t.Fatal("raw query bytes changed")
	}
	for _, legacy := range []bool{false, true} {
		f := cipherFormat(mediaBase+"?n=a%2Bb", "abc", "")
		if legacy {
			f.Cipher = f.SignatureCipher
			f.SignatureCipher = ""
		}
		got, e := resolveOne(t, c, f)
		if e != nil {
			t.Fatal(e)
		}
		u, _ := url.Parse(got)
		if u.Query().Get("signature") != "cba" || u.Query().Get("n") != "ok_a+b" || u.Query().Get("pot") != "origin+GVS/=" {
			t.Fatal("wrong transform or proof")
		}
	}
	// Direct sources need no compatible player if n is absent or empty.
	v := resolveVideo(t, c, resolveAsset(t, "bad script"), Format{URL: mediaBase + "?flag&n=&other=%252f"})
	got, e = c.Resolve(context.Background(), v, v.Formats()[0])
	if e != nil || got != mediaBase+"?flag&n=&other=%252f&pot=origin%2BGVS%2F%3D" {
		t.Fatal(e)
	}
	// The embedded URL is decoded once: its literal percent sequence stays encoded.
	got, e = resolveOne(t, c, cipherFormat(mediaBase+"?unrelated=%2526n%253Devil", "abc", "sig"))
	if e != nil || !strings.Contains(got, "unrelated=%2526n%253Devil") {
		t.Fatal("double decoding")
	}
}
func TestResolveCipherAndURLFailures(t *testing.T) {
	c := resolveClient(t)
	base := cipherFormat(mediaBase, "abc", "").SignatureCipher
	cases := []struct {
		name string
		f    Format
		want error
	}{
		{"missing", Format{}, ErrUnsupportedFormat},
		{"selected-cipher-fails", Format{SignatureCipher: "url=%xx", Cipher: base, URL: mediaBase}, ErrInvalidCipher},
		{"legacy-precedence", Format{Cipher: "url=%xx", URL: mediaBase}, ErrInvalidCipher},
		{"duplicate-url", Format{SignatureCipher: base + "&url=x"}, ErrInvalidCipher},
		{"duplicate-s", Format{SignatureCipher: base + "&s=x"}, ErrInvalidCipher},
		{"duplicate-sp", Format{SignatureCipher: base + "&sp=sig&sp=sig"}, ErrInvalidCipher},
		{"empty-sp", Format{SignatureCipher: base + "&sp="}, ErrInvalidCipher},
		{"missing-s", Format{SignatureCipher: "url=" + url.QueryEscape(mediaBase)}, ErrInvalidCipher},
		{"empty-url", Format{SignatureCipher: "url=&s=a"}, ErrInvalidCipher},
		{"unknown-outer", Format{SignatureCipher: base + "&signature=x"}, ErrInvalidCipher},
	}
	for _, sp := range []string{"n", "pot", "v", "s", "sp", "lsig", "sparams", "lsparams", "expire", "a&b", "x=y", "1x"} {
		cases = append(cases, struct {
			name string
			f    Format
			want error
		}{"bad-sp-" + sp, cipherFormat(mediaBase, "abc", sp), ErrInvalidCipher})
	}
	for _, raw := range []string{"http://rr1.googlevideo.com/videoplayback", "https://evilgooglevideo.com/videoplayback", "https://googlevideo.com/videoplayback", "https://rr1.googlevideo.com.evil/videoplayback", "https://user@rr1.googlevideo.com/videoplayback", "https://rr1.googlevideo.com:443/videoplayback", mediaBase + "#", mediaBase + "#frag", "//rr1.googlevideo.com/videoplayback", "https:opaque", mediaBase + "/other", mediaBase + "?n=%zz", mediaBase + "?x=unescaped space", mediaBase + "?x=é", mediaBase + "?n=a&%6e=b", mediaBase + "?n", mediaBase + "?pot=foreign", mediaBase + "?pot=x&pot=y", mediaBase + "?s=raw", mediaBase + "?sp=signature", mediaBase + "?lsig=a&lsig=b", mediaBase + "?lsig=", mediaBase + "?sig=&signature=x", mediaBase + "?sig=x&signature=y", mediaBase + "?expire=wat", mediaBase + "?n=abc&sparams=n", mediaBase + "?lsparams=pot"} {
		cases = append(cases, struct {
			name string
			f    Format
			want error
		}{fmt.Sprint("bad-url-", len(cases)), Format{URL: raw}, ErrInvalidURL})
	}
	cases = append(cases, struct {
		name string
		f    Format
		want error
	}{"expired", Format{URL: mediaBase + "?expire=1"}, ErrExpiredURL})
	for _, raw := range []string{mediaBase + "?signature=existing", mediaBase + "?sig=existing", mediaBase + "?sparams=custom", mediaBase + "?custom=existing"} {
		cases = append(cases, struct {
			name string
			f    Format
			want error
		}{fmt.Sprint("signed-", len(cases)), cipherFormat(raw, "abc", "custom"), ErrInvalidCipher})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, e := resolveOne(t, c, tc.f)
			if !errors.Is(e, tc.want) || out != "" {
				t.Fatalf("error=%v want %v", e, tc.want)
			}
			if strings.Contains(e.Error(), "origin+") || strings.Contains(e.Error(), "googlevideo") {
				t.Fatal("secret in error")
			}
		})
	}
	v := resolveVideo(t, c, resolveAsset(t, ""), Format{URL: mediaBase})
	v.snapshot.tokens.GVS = ""
	if _, e := c.Resolve(context.Background(), v, v.Formats()[0]); !errors.Is(e, ErrMissingGVS) {
		t.Fatal(e)
	}
	for _, input := range []string{"throw", "hang"} {
		v := resolveVideo(t, c, resolveAsset(t, ""), Format{URL: mediaBase + "?n=" + input})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		out, e := c.Resolve(ctx, v, v.Formats()[0])
		cancel()
		if out != "" || !errors.Is(e, ErrN) {
			t.Fatal(e)
		}
	}
}
func TestResolveProofAndSourceBinding(t *testing.T) {
	c := resolveClient(t)
	v := resolveVideo(t, c, resolveAsset(t, ""), Format{URL: mediaBase + "?n=x"})
	other := resolveVideo(t, c, v.player, v.formats[0])
	other.snapshot.tokens.GVS = "second-origin"
	for _, v := range []*Video{v, other} {
		out, e := c.Resolve(context.Background(), v, v.Formats()[0])
		if e != nil {
			t.Fatal(e)
		}
		u, _ := url.Parse(out)
		if u.Query().Get("pot") != v.snapshot.tokens.GVS {
			t.Fatal("wrong origin")
		}
	}
	// Existing identical proof is preserved; conflicting proofs fail above.
	f := Format{URL: mediaBase + "?pot=origin%2bGVS%2f%3d&flag"}
	out, e := resolveOne(t, c, f)
	if e != nil || out != f.URL {
		t.Fatal("identical proof changed")
	}
}
func TestResolveFreshRuntimeAndNoMutation(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	src := strings.Replace(string(a.source), "var constants=", "var count=0;var constants=", 1)
	src = strings.Replace(src, `"ok_"+this.values.n`, `"ok_"+(++count)+this.values.n`, 1)
	v := resolveVideo(t, c, resolveAsset(t, src), cipherFormat(mediaBase+"?n=x", "abc", ""))
	before := v.Formats()
	for i := 0; i < 3; i++ {
		out, e := c.Resolve(context.Background(), v, before[0])
		if e != nil {
			t.Fatal(e)
		}
		u, _ := url.Parse(out)
		if u.Query().Get("n") != "ok_1x" {
			t.Fatal("state not isolated")
		}
	}
	if !reflect.DeepEqual(before, v.Formats()) {
		t.Fatal("Resolve mutated video")
	}
}
func TestGetVideoResolveNoAdditionalCalls(t *testing.T) {
	f := newP2Transport(t)
	f.script = resolveAsset(t, "").source
	f.response = []byte(`{"playabilityStatus":{"status":"OK"},"streamingData":{"formats":[{"itag":1,"signatureCipher":"` + cipherFormat(mediaBase+"?n=a", "abc", "").SignatureCipher + `"}]}}`)
	c := newP2Client(t, f)
	v, e := c.GetVideo(context.Background(), "aaaaaaaaaaa")
	if e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	before := len(f.requests)
	f.mu.Unlock()
	f.base.mu.Lock()
	baseBefore := fmt.Sprint(f.base.calls)
	f.base.mu.Unlock()
	for i := 0; i < 3; i++ {
		if _, e := c.Resolve(context.Background(), v, v.Formats()[0]); e != nil {
			t.Fatal(e)
		}
	}
	f.mu.Lock()
	after := len(f.requests)
	f.mu.Unlock()
	f.base.mu.Lock()
	baseAfter := fmt.Sprint(f.base.calls)
	f.base.mu.Unlock()
	if before != after || baseBefore != baseAfter {
		t.Fatal("Resolve made extra requests")
	}
}

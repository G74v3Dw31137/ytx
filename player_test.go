package ytx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/G74v3Dw31137/potter"
)

func TestPlayerScriptDiscovery(t *testing.T) {
	const target = "https://m.youtube.com/s/player/id/base.js"
	for _, tc := range []struct{ name, object, base, want string }{
		{"relative", `{"jsUrl":"/s/player/id/base.js"}`, "https://m.youtube.com/", target},
		{"document-relative", `{"jsUrl":"../../s/player/id/base.js"}`, "https://m.youtube.com/watch/other", target},
		{"absolute", `{"jsUrl":"https://www.youtube.com/s/player/id/base.js"}`, "https://m.youtube.com/", "https://www.youtube.com/s/player/id/base.js"},
		{"scheme-relative", `{"jsUrl":"//m.youtube.com/s/player/id/base.js"}`, "https://www.youtube.com/", target},
		{"escaped", `{"PLAYER_JS_URL":"\/s\/player\/id\/base.js"}`, "https://m.youtube.com/", target},
		{"watch", `{"WEB_PLAYER_CONTEXT_CONFIGS":{"WEB_PLAYER_CONTEXT_CONFIG_ID_MWEB_WATCH":{"jsUrl":"/s/player/id/base.js"}}}`, "https://m.youtube.com/", target},
		{"consistent", `{"jsUrl":"/s/player/id/base.js","PLAYER_JS_URL":"https://m.youtube.com/s/player/id/base.js"}`, "https://m.youtube.com/", target},
		{"missing", `{}`, "https://m.youtube.com/", ""},
		{"empty", `{"jsUrl":""}`, "https://m.youtube.com/", ""},
		{"wrong-type", `{"jsUrl":3}`, "https://m.youtube.com/", ""},
		{"null", `{"jsUrl":null}`, "https://m.youtube.com/", ""},
		{"malformed", `{"jsUrl":`, "https://m.youtube.com/", ""},
		{"bad-escape", `{"jsUrl":"\x2fs/player/id/base.js"}`, "https://m.youtube.com/", ""},
		{"conflict", `{"jsUrl":"/s/player/id/base.js","PLAYER_JS_URL":"/s/player/other/base.js"}`, "https://m.youtube.com/", ""},
		{"unsafe-base", `{"jsUrl":"/s/player/id/base.js"}`, "https://evil.invalid/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := playerScriptURL([]byte(`<script>ytcfg.set(`+tc.object+`);</script>`), tc.base)
			if tc.want == "" {
				if !errors.Is(err, ErrPlayerScript) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("URL = %q, %v", got, err)
			}
		})
	}
	for _, ref := range []string{
		"http://m.youtube.com/s/player/id/base.js", "https://evil.invalid/s/player/id/base.js",
		"https://m.youtube.com.evil.invalid/s/player/id/base.js", "https://user@m.youtube.com/s/player/id/base.js",
		"https://m.youtube.com:443/s/player/id/base.js", "javascript:alert(1)", "/s/player/id/base.js?key=secret",
		"/s/player/id/base.js#fragment", "/s/player/id/../base.js", "/s/player/id/%2e%2e/base.js",
		"/s/player/id%2fextra/base.js", "/s/player//base.js", "/s/player/base.js", "/other/base.js", " /s/player/id/base.js", "%zz",
	} {
		t.Run(ref, func(t *testing.T) {
			b, _ := json.Marshal(map[string]string{"jsUrl": ref})
			_, err := playerScriptURL([]byte(`<script>ytcfg.set(`+string(b)+`);</script>`), "https://m.youtube.com/")
			if !errors.Is(err, ErrPlayerScript) {
				t.Fatal("unsafe locator accepted")
			}
		})
	}
	for _, body := range []string{
		`<script type="application/json">ytcfg.set({"jsUrl":"/s/player/id/base.js"});</script>`,
		`<template><script>ytcfg.set({"jsUrl":"/s/player/id/base.js"});</script></template>`,
		`<script>ytcfg.set({});ytcfg.set({"jsUrl":"/s/player/id/base.js"});</script>`,
	} {
		if _, err := playerScriptURL([]byte(body), "https://m.youtube.com/"); err == nil {
			t.Fatal("inert/later locator accepted")
		}
	}
}

func TestSTS(t *testing.T) {
	for _, source := range []string{`var x={signatureTimestamp:20760};`, `x={sts:20760,other:1}`, `x={"signatureTimestamp" : 20760 }`, `x={'sts':20760}`, "x={\n sts : 20760\n}"} {
		if got, err := extractSTS([]byte(source)); err != nil || got != 20760 {
			t.Fatalf("STS = %d, %v", got, err)
		}
	}
	for _, source := range []string{"", `x={signatureTimestamp:0}`, `x={sts:-1}`, `x={sts:1.5}`, `x={sts:20760+1}`, `x={sts:1e5}`, `x={sts:99999999999999999999}`, `x={othersts:20760}`, `x={sts:"20760"}`, `x={sts:20760oops}`, `x={sts:20760`} {
		if _, err := extractSTS([]byte(source)); !errors.Is(err, ErrPlayerScript) {
			t.Fatalf("accepted unsupported STS %q", source)
		}
	}
}

type trackedMetadataBody struct {
	io.Reader
	closed, reads *atomic.Int32
	readError     error
}

func (b *trackedMetadataBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	if b.readError != nil {
		return 0, b.readError
	}
	return b.Reader.Read(p)
}
func (b *trackedMetadataBody) Close() error { b.closed.Add(1); return nil }

type p2Transport struct {
	base             *offlineTransport
	mu               sync.Mutex
	script, response []byte
	requests         []*http.Request
	bodies           [][]byte
	closed, reads    atomic.Int32
	hook             func(*http.Request) (*http.Response, error)
}

func newP2Transport(t *testing.T) *p2Transport {
	t.Helper()
	script, err := os.ReadFile("testdata/player-sts.js")
	if err != nil {
		t.Fatal(err)
	}
	response, err := os.ReadFile("testdata/player-response.json")
	if err != nil {
		t.Fatal(err)
	}
	return &p2Transport{base: newOfflineTransport(t, false), script: script, response: response}
}

func (f *p2Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/youtubei/v1/player" && !strings.HasPrefix(r.URL.Path, "/s/player/") {
		return f.base.RoundTrip(r)
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(r.Context()))
	f.bodies = append(f.bodies, body)
	hook := f.hook
	data := f.script
	if r.URL.Path == "/youtubei/v1/player" {
		data = f.response
	}
	f.mu.Unlock()
	if hook != nil {
		return hook(r)
	}
	return f.reply(r, http.StatusOK, data), nil
}

func (f *p2Transport) reply(r *http.Request, status int, raw []byte) *http.Response {
	return &http.Response{Request: r, StatusCode: status, Header: make(http.Header), Body: &trackedMetadataBody{Reader: bytes.NewReader(raw), closed: &f.closed, reads: &f.reads}}
}

func newP2Client(t *testing.T, f *p2Transport) *Client {
	t.Helper()
	c, err := NewClient(WithHTTPClient(&http.Client{Transport: f}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func checkPlayerRequest(t *testing.T, r *http.Request, raw []byte, s SessionSnapshot, id string) {
	t.Helper()
	if r.Method != "POST" || r.URL.String() != playerEndpoint || r.URL.Query().Get("key") != "" {
		t.Fatal("incorrect Player endpoint")
	}
	for key, want := range map[string]string{"User-Agent": potter.UserAgent, "Origin": "https://www.youtube.com", "Content-Type": "application/json", "X-Youtube-Client-Name": "2", "X-Youtube-Client-Version": s.config.ClientVersion, "X-Goog-Visitor-Id": s.config.VisitorData} {
		if r.Header.Get(key) != want {
			t.Errorf("header %s differs", key)
		}
	}
	var got map[string]json.RawMessage
	if json.Unmarshal(raw, &got) != nil {
		t.Fatal("invalid request JSON")
	}
	var contextGot, contextWant any
	_ = json.Unmarshal(got["context"], &contextGot)
	_ = json.Unmarshal(s.config.Context, &contextWant)
	if !reflect.DeepEqual(contextGot, contextWant) {
		t.Fatal("full context not preserved")
	}
	var integrity struct {
		POToken string `json:"poToken"`
	}
	_ = json.Unmarshal(got["serviceIntegrityDimensions"], &integrity)
	if integrity.POToken != s.tokens.Player || integrity.POToken == s.tokens.GVS {
		t.Fatal("wrong proof attached")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(integrity.POToken, "="))
	if err != nil || string(decoded) != "fixture:1:"+id {
		t.Fatal("Player proof not bound to video")
	}
	for key, want := range map[string]string{"videoId": `"` + id + `"`, "contentCheckOk": "true", "racyCheckOk": "true"} {
		if string(got[key]) != want {
			t.Errorf("wrong request field %s", key)
		}
	}
	var playback any
	_ = json.Unmarshal(got["playbackContext"], &playback)
	wantPlayback := map[string]any{"contentPlaybackContext": map[string]any{"signatureTimestamp": float64(20760), "html5Preference": "HTML5_PREF_WANTS"}}
	if !reflect.DeepEqual(playback, wantPlayback) {
		t.Fatal("wrong playback context")
	}
}

func TestGetVideo(t *testing.T) {
	for _, visitorBound := range []bool{false, true} {
		t.Run(fmt.Sprint(visitorBound), func(t *testing.T) {
			f := newP2Transport(t)
			f.base.page = fixturePage("test-visitor", "original", visitorBound)
			c := newP2Client(t, f)
			v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
			if err != nil {
				t.Fatal(err)
			}
			info := v.Info()
			if info.ID != "aaaaaaaaaaa" || info.Title != "Fixture video" || info.Author != "Fixture author" || info.ChannelID != "UCfixture" || info.Duration != 123*time.Second || info.PlaybackStatus != "OK" {
				t.Fatal("wrong metadata")
			}
			formats := v.Formats()
			if len(formats) != 3 || !formats[0].HasAudio || !formats[0].HasVideo || formats[0].Adaptive || !formats[1].HasAudio || formats[1].HasVideo || !formats[1].Adaptive || formats[2].HasAudio || !formats[2].HasVideo {
				t.Fatal("wrong format classification")
			}
			if formats[0].URL == "" || formats[1].SignatureCipher == "" || formats[1].URL != "" || formats[1].ContentLength != "2000000" || formats[1].AverageBitrate != 128000 || formats[1].AudioTrack.ID != "en.0" {
				t.Fatal("lost wire format fields")
			}
			if len(f.requests) != 2 || f.closed.Load() != 2 {
				t.Fatal("wrong downstream requests/closure")
			}
			checkPlayerRequest(t, f.requests[1], f.bodies[1], v.snapshot, info.ID)
			if f.requests[0].Header.Get("User-Agent") != potter.UserAgent || f.requests[0].Header.Get("Origin") != "https://www.youtube.com" {
				t.Fatal("wrong asset headers")
			}
			p := v.Provenance()
			if p.Generation != 1 || p.VideoID != info.ID || p.DocumentURL != "https://m.youtube.com/" || p.PlayerJSURL != "https://m.youtube.com/s/player/original/base.js" || p.PlayerJSFinalURL != p.PlayerJSURL || p.SignatureTimestamp != 20760 || len(p.PlayerJSSHA256) != 64 {
				t.Fatal("lost provenance")
			}
			if !bytes.Equal(v.player.source, f.script) || !bytes.Equal(v.response, f.response) || v.snapshot.tokens.GVS == "" {
				t.Fatal("lost P3 input")
			}
			formats[0].Codecs[0] = "changed"
			formats[1].AudioTrack.ID = "changed"
			formats[1].SignatureCipher = "changed"
			info.Title = "changed"
			if v.Formats()[0].Codecs[0] == "changed" || v.Formats()[1].AudioTrack.ID == "changed" || v.Info().Title == "changed" {
				t.Fatal("mutable internal state exposed")
			}
			for _, value := range []any{v, *v, v.Formats()[0], v.snapshot} {
				if !strings.Contains(fmt.Sprintf("%+v", value), "redacted") {
					t.Fatal("sensitive formatting")
				}
			}
			c.Close()
			if v.Provenance() != p || !bytes.Equal(v.player.source, f.script) {
				t.Fatal("result changed after Close")
			}
			f.base.checkCalls(t, 1, 1, 1, 0)
		})
	}
}

func TestPlayerParsingFailuresAndLegacyCipher(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
		partial    bool
	}{
		{"json", `{`, ErrPlayerResponse, false}, {"null", `null`, ErrPlayerResponse, false},
		{"missing-status", `{}`, ErrPlayerResponse, false},
		{"wrong-video", `{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"wrong"}}`, ErrPlayerResponse, false},
		{"no-streaming", `{"playabilityStatus":{"status":"OK"}}`, ErrNoFormats, true},
		{"empty-arrays", `{"playabilityStatus":{"status":"OK"},"streamingData":{"formats":[]}}`, ErrNoFormats, true},
		{"bad-format", `{"playabilityStatus":{"status":"OK"},"streamingData":{"formats":[{"itag":"18"}]}}`, ErrPlayerResponse, false},
		{"null-format", `{"playabilityStatus":{"status":"OK"},"streamingData":{"formats":[null]}}`, ErrPlayerResponse, false},
		{"playback", `{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"fixture reason"}}`, &PlaybackError{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newP2Transport(t)
			f.response = []byte(tc.body)
			c := newP2Client(t, f)
			v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
			var playback *PlaybackError
			if _, ok := tc.want.(*PlaybackError); ok {
				if !errors.As(err, &playback) || playback.Status != "LOGIN_REQUIRED" || playback.Reason != "fixture reason" || v.Info().PlaybackReason != playback.Reason {
					t.Fatal("wrong playback error")
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v", err)
			}
			if (v != nil) != tc.partial || f.closed.Load() != 2 || len(f.requests) != 2 {
				t.Fatal("result/closure/retry mismatch")
			}
		})
	}
	raw := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"lengthSeconds":"9999999999999999999"},"streamingData":{"expiresInSeconds":"3600","adaptiveFormats":[{"itag":999,"cipher":"s=opaque&url=unresolved","initRange":{"start":"0","end":"99"},"indexRange":{"start":"100","end":"199"}}]}}`)
	v, err := parseVideo(raw, SessionSnapshot{}, playerAsset{}, "requested")
	if err != nil || v.Info().Duration != 0 || v.Info().LengthSeconds != "9999999999999999999" || v.Info().ExpiresInSeconds != "3600" {
		t.Fatal("lost raw metadata")
	}
	f := v.Formats()[0]
	if f.Cipher != "s=opaque&url=unresolved" || f.HasAudio || f.HasVideo || f.ContentLength != "" || len(f.Codecs) != 0 {
		t.Fatal("invented or lost format metadata")
	}
	f.InitRange.Start = "changed"
	f.IndexRange.End = "changed"
	if v.Formats()[0].InitRange.Start != "0" || v.Formats()[0].IndexRange.End != "199" {
		t.Fatal("shared range pointers")
	}
}

func TestGetVideoHTTPFailures(t *testing.T) {
	transportErr := errors.New("PRIVATE transport detail")
	for _, stage := range []string{"script", "player"} {
		for _, mode := range []string{"status", "transport", "read", "size", "redirect", "sts"} {
			if stage == "player" && mode == "sts" {
				continue
			}
			t.Run(stage+"/"+mode, func(t *testing.T) {
				f := newP2Transport(t)
				f.hook = func(r *http.Request) (*http.Response, error) {
					isScript := strings.HasPrefix(r.URL.Path, "/s/player/")
					if isScript != (stage == "script") {
						return f.reply(r, 200, f.script), nil
					}
					switch mode {
					case "transport":
						return nil, transportErr
					case "status":
						return f.reply(r, 403, []byte("PRIVATE error body")), nil
					case "read":
						resp := f.reply(r, 200, nil)
						resp.Body.(*trackedMetadataBody).readError = io.ErrUnexpectedEOF
						return resp, nil
					case "size":
						return f.reply(r, 200, bytes.Repeat([]byte{' '}, maxMetadataBytes+1)), nil
					case "redirect":
						resp := f.reply(r, 307, nil)
						resp.Header.Set("Location", "https://evil.invalid/s/player/id/base.js")
						return resp, nil
					default:
						return f.reply(r, 200, []byte(`var missingSTS = true;`)), nil
					}
				}
				c := newP2Client(t, f)
				v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
				if err == nil || v != nil {
					t.Fatal("expected failure without result")
				}
				if strings.Contains(fmt.Sprint(err), "PRIVATE") || strings.Contains(fmt.Sprint(err), "evil.invalid") {
					t.Fatal("error leaks request data")
				}
				switch mode {
				case "transport":
					if !errors.Is(err, transportErr) {
						t.Fatal("lost transport cause")
					}
				case "status":
					var status *HTTPError
					if !errors.As(err, &status) || status.StatusCode != 403 {
						t.Fatal("lost HTTP status")
					}
				case "size":
					if !errors.Is(err, ErrResponseTooLarge) {
						t.Fatal("lost size error")
					}
				case "read":
					if !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatal("lost read error")
					}
				case "sts":
					if !errors.Is(err, ErrPlayerScript) {
						t.Fatal("lost STS error")
					}
				}
				want := 1
				if stage == "player" {
					want = 2
				}
				if len(f.requests) != want {
					t.Fatal("unexpected request or retry")
				}
				closed := want
				if mode == "transport" {
					closed--
				}
				if int(f.closed.Load()) != closed {
					t.Fatal("response body not closed exactly once")
				}
				f.base.checkCalls(t, 1, 1, 1, 0)
			})
		}
	}
}

func TestMetadataBoundsAndRedirectPolicy(t *testing.T) {
	f := newP2Transport(t)
	var policyCalls atomic.Int32
	hc := &http.Client{Transport: f, CheckRedirect: func(*http.Request, []*http.Request) error { policyCalls.Add(1); return nil }}
	c, err := NewClient(WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f.hook = func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/s/player/original/base.js" {
			resp := f.reply(r, 302, nil)
			resp.Header.Set("Location", "/s/player/redirected/base.js")
			return resp, nil
		}
		if r.URL.Path == "/s/player/redirected/base.js" {
			return f.reply(r, 200, f.script), nil
		}
		return f.reply(r, 200, f.response), nil
	}
	v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if policyCalls.Load() != 1 || len(f.requests) != 3 || f.closed.Load() != 3 || v.Provenance().PlayerJSURL != "https://m.youtube.com/s/player/original/base.js" || v.Provenance().PlayerJSFinalURL != "https://m.youtube.com/s/player/redirected/base.js" {
		t.Fatal("lost redirect policy/identity/closure")
	}
	// Even redirects to an approved YouTube hostname cannot replay the PO body
	// to a different endpoint. Caller restrictions are additionally preserved.
	f.hook = func(r *http.Request) (*http.Response, error) {
		resp := f.reply(r, 307, nil)
		resp.Header.Set("Location", "https://www.youtube.com/other")
		return resp, nil
	}
	_, err = c.requestPlayer(context.Background(), v.snapshot, 20760, "aaaaaaaaaaa")
	if err == nil || len(f.requests) != 4 {
		t.Fatal("Player redirect was followed")
	}
	f.hook = func(r *http.Request) (*http.Response, error) {
		return f.reply(r, 200, bytes.Repeat([]byte{'x'}, maxMetadataBytes)), nil
	}
	raw, _, err := c.readMetadata(context.Background(), "player script", "GET", v.player.url, nil)
	if err != nil || len(raw) != maxMetadataBytes {
		t.Fatal("exact metadata bound rejected")
	}
	f.hook = func(r *http.Request) (*http.Response, error) {
		resp := f.reply(r, 302, nil)
		resp.Header.Set("Location", "/s/player/redirected/base.js")
		return resp, nil
	}
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("caller policy") }
	before := len(f.requests)
	_, err = c.downloadPlayer(context.Background(), v.player.url)
	if err == nil || len(f.requests) != before+1 {
		t.Fatal("caller redirect restriction bypassed")
	}
}

func TestGetVideoMissingPolicyAndLocalErrors(t *testing.T) {
	f := newOfflineTransport(t, false)
	pc, err := potter.NewClient(potter.WithHTTPClient(&http.Client{Transport: f}))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	c, err := NewClient(WithPotterClient(pc))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa"); v != nil || !errors.Is(err, ErrHTTPPolicy) {
		t.Fatal("missing policy not rejected")
	}
	f.checkCalls(t, 0, 0, 0, 0)
	for _, ctx := range []context.Context{nil, context.Background()} {
		if v, err := c.GetVideo(ctx, ""); v != nil || err == nil {
			t.Fatal("local validation failed")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetVideo(ctx, "aaaaaaaaaaa"); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation")
	}
	c.Close()
	if _, err := c.GetVideo(context.Background(), "aaaaaaaaaaa"); !errors.Is(err, ErrClosed) {
		t.Fatal("closed client admitted call")
	}
	// Snapshot remains supported with borrowed Potter and no downstream policy.
	if _, err := pc.TokensWithSession(context.Background(), "aaaaaaaaaaa"); err != nil {
		t.Fatal("borrowed Potter closed")
	}
}

func TestGetVideoDiscoveryFailureStopsDownstream(t *testing.T) {
	for _, ref := range []string{"", "https://evil.invalid/s/player/id/base.js"} {
		t.Run(ref, func(t *testing.T) {
			f := newP2Transport(t)
			f.base.page = bytes.ReplaceAll(f.base.page, []byte("/s/player/original/base.js"), []byte(ref))
			c := newP2Client(t, f)
			v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
			if v != nil || !errors.Is(err, ErrPlayerScript) || len(f.requests) != 0 {
				t.Fatal("discovery failure reached downstream")
			}
			f.base.checkCalls(t, 1, 1, 1, 0)
		})
	}
}

func TestGetVideoSessionReplacement(t *testing.T) {
	for _, sameIdentity := range []bool{false, true} {
		t.Run(fmt.Sprint(sameIdentity), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newP2Transport(t)
				c := newP2Client(t, f)
				first, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
				if err != nil {
					t.Fatal(err)
				}
				firstSource := append([]byte(nil), first.player.source...)
				visitor := "replacement-visitor"
				if sameIdentity {
					visitor = "test-visitor"
				}
				f.base.mu.Lock()
				f.base.page = fixturePage(visitor, "replacement", true)
				f.base.redirect = true
				f.base.mu.Unlock()
				time.Sleep(time.Minute)
				f.mu.Lock()
				f.script = []byte(`x={signatureTimestamp:20761};`)
				f.mu.Unlock()
				next, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
				if err != nil {
					t.Fatal(err)
				}
				if first.Provenance().Generation != 1 || next.Provenance().Generation != 2 || next.Provenance().SignatureTimestamp != 20761 || next.Provenance().PlayerJSURL != "https://www.youtube.com/s/player/replacement/base.js" || first.Provenance().PlayerJSURL != "https://m.youtube.com/s/player/original/base.js" || !bytes.Equal(first.player.source, firstSource) {
					t.Fatal("mixed session/script generation")
				}
				if first.snapshot.tokens == next.snapshot.tokens || next.snapshot.tokens.VisitorData != visitor || !bytes.Contains(next.snapshot.config.Context, []byte("replacement")) {
					t.Fatal("lost originating token/context association")
				}
				oldURL, err := discoverPlayerScript(first.snapshot)
				if err != nil || oldURL != first.player.url {
					t.Fatal("old discovery consulted current session")
				}
				checkPlayerRequest(t, f.requests[1], f.bodies[1], first.snapshot, "aaaaaaaaaaa")
				// Replacement's body must use its own STS, visitor, full context and proof.
				var body map[string]json.RawMessage
				_ = json.Unmarshal(f.bodies[3], &body)
				if !bytes.Contains(body["playbackContext"], []byte("20761")) || f.requests[3].Header.Get("X-Goog-Visitor-Id") != visitor {
					t.Fatal("mixed replacement request")
				}
				var integrity struct {
					POToken string `json:"poToken"`
				}
				_ = json.Unmarshal(body["serviceIntegrityDimensions"], &integrity)
				if integrity.POToken != next.snapshot.tokens.Player || bytes.Equal(body["context"], json.RawMessage(first.snapshot.config.Context)) {
					t.Fatal("mixed replacement proof/context")
				}
				f.base.checkCalls(t, 2, 1, 2, 1)
			})
		})
	}
}

func TestGetVideoCancellationAndClose(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		for _, stage := range []string{"script", "player"} {
			for _, closeClient := range []bool{false, true} {
				t.Run(fmt.Sprintf("borrowed=%t/%s/close=%t", borrowed, stage, closeClient), func(t *testing.T) {
					f := newP2Transport(t)
					hc := &http.Client{Transport: f}
					var options []Option
					if borrowed {
						pc, err := potter.NewClient(potter.WithHTTPClient(hc))
						if err != nil {
							t.Fatal(err)
						}
						defer pc.Close()
						options = append(options, WithPotterClient(pc))
					}
					options = append(options, WithHTTPClient(hc))
					c, err := NewClient(options...)
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					entered := make(chan struct{})
					f.hook = func(r *http.Request) (*http.Response, error) {
						if strings.HasPrefix(r.URL.Path, "/s/player/") != (stage == "script") {
							return f.reply(r, 200, f.script), nil
						}
						close(entered)
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan error, 1)
					go func() {
						v, err := c.GetVideo(ctx, "aaaaaaaaaaa")
						if v != nil {
							done <- errors.New("unexpected result")
							return
						}
						done <- err
					}()
					select {
					case <-entered:
					case <-time.After(5 * time.Second):
						t.Fatal("request did not start")
					}
					closed := make(chan struct{})
					if closeClient {
						go func() { c.Close(); close(closed) }()
					} else {
						cancel()
						close(closed)
					}
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("error = %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("GetVideo did not cancel")
					}
					select {
					case <-closed:
					case <-time.After(5 * time.Second):
						t.Fatal("Close did not join GetVideo")
					}
					if borrowed {
						if _, err := c.potter.TokensWithSession(context.Background(), "aaaaaaaaaaa"); err != nil {
							t.Fatal("borrowed Potter closed")
						}
					}
					f.base.checkCalls(t, 1, 1, 1, 0)
				})
			}
		}
	}
}

func TestConcurrentGetVideo(t *testing.T) {
	f := newP2Transport(t)
	c := newP2Client(t, f)
	if _, err := c.Snapshot(context.Background(), "aaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
			if err != nil || v.Provenance().Generation != 1 || len(v.Formats()) != 3 {
				t.Error("concurrent metadata failed")
				return
			}
			formats := v.Formats()
			formats[0].Codecs[0] = "private copy"
		})
	}
	wg.Wait()
	if len(f.requests) != 9 || f.closed.Load() != 9 {
		t.Fatal("incorrect concurrent request/closure count")
	}
	f.base.checkCalls(t, 1, 1, 1, 0)
}

func TestSyntheticProtocolDiscovery(t *testing.T) {
	const locator = `{"PLAYER_JS_URL":"/s/player/fixture/base.js","WEB_PLAYER_CONTEXT_CONFIGS":{"WEB_PLAYER_CONTEXT_CONFIG_ID_MWEB_WATCH":{"jsUrl":"/s/player/fixture/base.js"}}}`
	resolved, err := playerScriptURL([]byte("<script>ytcfg.set("+locator+");</script>"), "https://m.youtube.com/")
	if err != nil || resolved != "https://m.youtube.com/s/player/fixture/base.js" {
		t.Fatalf("player URL = %q, error = %v", resolved, err)
	}
	source, err := os.ReadFile("testdata/player-sts.js")
	if err != nil {
		t.Fatal(err)
	}
	sts, err := extractSTS(source)
	if err != nil || sts != 20760 {
		t.Fatalf("STS = %d, error = %v", sts, err)
	}
}

// This output is available with go test -v for the protocol artifact inventory.
// It is an actual offline request observation, explicitly marked synthetic.
func TestP2ProtocolEvidence(t *testing.T) {
	f := newP2Transport(t)
	c := newP2Client(t, f)
	v, err := c.GetVideo(context.Background(), "aaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal(f.bodies[1], &body) != nil {
		t.Fatal("invalid body")
	}
	body["serviceIntegrityDimensions"].(map[string]any)["poToken"] = "[REDACTED:PLAYER_PO_TOKEN]"
	body["context"].(map[string]any)["client"].(map[string]any)["visitorData"] = "[REDACTED:VISITOR_DATA]"
	headers := f.requests[1].Header.Clone()
	headers.Set("X-Goog-Visitor-Id", "[REDACTED:VISITOR_DATA]")
	output, err := json.Marshal(map[string]any{
		"evidence_kind": "synthetic_fixture_offline_observation", "method": f.requests[1].Method,
		"endpoint": f.requests[1].URL.String(), "headers": headers, "body": body,
		"synthetic_response_status": 200, "provenance": v.Provenance(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("P2_EVIDENCE_JSON %s", output)
}

type canceledMetadataReader struct {
	ctx     context.Context
	entered chan struct{}
}

func (r *canceledMetadataReader) Read([]byte) (int, error) {
	close(r.entered)
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestGetVideoCloseDuringBodyRead(t *testing.T) {
	for _, stage := range []string{"script", "player"} {
		t.Run(stage, func(t *testing.T) {
			f := newP2Transport(t)
			c := newP2Client(t, f)
			entered := make(chan struct{})
			f.hook = func(r *http.Request) (*http.Response, error) {
				if strings.HasPrefix(r.URL.Path, "/s/player/") != (stage == "script") {
					return f.reply(r, 200, f.script), nil
				}
				resp := f.reply(r, 200, nil)
				resp.Body.(*trackedMetadataBody).Reader = &canceledMetadataReader{r.Context(), entered}
				return resp, nil
			}
			done := make(chan error, 1)
			go func() { _, err := c.GetVideo(context.Background(), "aaaaaaaaaaa"); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("body read did not start")
			}
			var wg sync.WaitGroup
			for range 3 {
				wg.Go(func() { c.Close() })
			}
			joined := make(chan struct{})
			go func() { wg.Wait(); close(joined) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("body read did not cancel")
			}
			select {
			case <-joined:
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent Close callers did not join")
			}
			want := int32(1)
			if stage == "player" {
				want = 2
			}
			if f.closed.Load() != want {
				t.Fatal("body leaked after cancellation")
			}
		})
	}
}

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
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/G74v3Dw31137/potter"
)

// This transport never delegates to a network transport. All successful tests
// exercise released Potter's real public API with synthetic, invalid proofs.
type offlineTransport struct {
	mu              sync.Mutex
	page, script    []byte
	redirect        bool
	calls           map[string]int
	transportClosed bool
}

func fixturePage(visitor, marker string, visitorBound bool) []byte {
	object := strings.ReplaceAll(validConfig, "test-visitor", visitor)
	object = strings.ReplaceAll(object, "original", marker)
	var cfg map[string]json.RawMessage
	_ = json.Unmarshal([]byte(object), &cfg)
	cfg["WEB_PLAYER_CONTEXT_CONFIGS"] = json.RawMessage(fmt.Sprintf(`{"WEB_PLAYER_CONTEXT_CONFIG_ID_MWEB_WATCH":{"serializedExperimentFlags":"html5_generate_content_po_token=%t"}}`, !visitorBound))
	// The player locator deliberately lives outside INNERTUBE_CONTEXT. P1
	// retains these bytes but does not discover or fetch this asset.
	cfg["jsUrl"], _ = json.Marshal("/s/player/" + marker + "/base.js")
	raw, _ := json.Marshal(cfg)
	return []byte(`<script>ytcfg.set(` + string(raw) + `);window.ytAtN({R:{bgChallenge:{program:'fixture-program',globalName:'testGuard',interpreterUrl:{privateDoNotAccessOrElseTrustedResourceUrlWrappedValue:'https://www.google.com/botguard/fixture.js'}}}});</script>`)
}

func newOfflineTransport(t *testing.T, visitorBound bool) *offlineTransport {
	t.Helper()
	script, err := os.ReadFile("testdata/protocol-mock.js")
	if err != nil {
		t.Fatal(err)
	}
	return &offlineTransport{page: fixturePage("test-visitor", "original", visitorBound), script: script, calls: make(map[string]int)}
}

func (f *offlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var kind string
	var body []byte
	header := make(http.Header)
	status := http.StatusOK
	switch {
	case r.Method == "GET" && r.URL.String() == "https://m.youtube.com/":
		kind, body = "homepage", f.page
		if f.redirect {
			status = http.StatusFound
			header.Set("Location", "https://www.youtube.com/")
			body = nil
		}
	case r.Method == "GET" && r.URL.String() == "https://www.youtube.com/" && f.redirect:
		kind, body = "redirected-homepage", f.page
	case r.Method == "GET" && r.URL.String() == "https://www.google.com/botguard/fixture.js":
		kind, body = "interpreter", f.script
	case r.Method == "POST" && r.URL.String() == "https://jnn-pa.googleapis.com/$rpc/google.internal.waa.v1.Waa/GenerateIT":
		kind, body = "integrity", []byte(`["VEVTVElU",60,30]`)
	default:
		return nil, errors.New("unexpected offline request")
	}
	f.calls[kind]++
	if r.Header.Get("User-Agent") != potter.UserAgent {
		return nil, errors.New("unexpected user agent")
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func (f *offlineTransport) CloseIdleConnections() {
	f.mu.Lock()
	f.transportClosed = true
	f.mu.Unlock()
}

func (f *offlineTransport) checkCalls(t *testing.T, homepage, interpreter, integrity, redirects int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for kind, want := range map[string]int{"homepage": homepage, "interpreter": interpreter, "integrity": integrity, "redirected-homepage": redirects} {
		if f.calls[kind] != want {
			t.Errorf("%s requests = %d, want %d", kind, f.calls[kind], want)
		}
	}
	if f.transportClosed {
		t.Error("caller transport was closed")
	}
}

func newOfflineClient(t *testing.T, f *offlineTransport) *Client {
	t.Helper()
	c, err := NewClient(WithHTTPClient(&http.Client{Transport: f}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func assertZeroSnapshot(t *testing.T, s SessionSnapshot) {
	t.Helper()
	if s.Tokens() != (potter.Tokens{}) || s.Generation() != 0 || s.DocumentURL() != "" || s.Config().Context != nil {
		t.Fatal("expected zero snapshot")
	}
	url, body := s.origin.Homepage()
	raw, err := io.ReadAll(body)
	if err != nil || url != "" || len(raw) != 0 || s.origin.Generation() != 0 || s.origin.Tokens() != (potter.Tokens{}) {
		t.Fatal("expected zero retained origin")
	}
}

func assertRetainedHomepage(t *testing.T, s SessionSnapshot, want []byte) {
	t.Helper()
	url, reader := s.origin.Homepage()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("retained homepage differs from originating response")
	}
	if url != s.DocumentURL() || s.origin.Generation() != s.Generation() || s.origin.Tokens() != s.Tokens() {
		t.Fatal("retained origin has inconsistent provenance")
	}
	cfg, err := parseHomepageConfig(got)
	if err != nil || string(cfg.Context) != string(s.Config().Context) {
		t.Fatal("retained homepage has inconsistent context")
	}
}

func TestClientSnapshotAndRequestBudget(t *testing.T) {
	for _, visitorBound := range []bool{false, true} {
		t.Run(fmt.Sprint(visitorBound), func(t *testing.T) {
			f := newOfflineTransport(t, visitorBound)
			c := newOfflineClient(t, f)
			f.checkCalls(t, 0, 0, 0, 0)
			first, err := c.Snapshot(context.Background(), "video-a")
			if err != nil {
				t.Fatal(err)
			}
			if first.Generation() != 1 || first.DocumentURL() != "https://m.youtube.com/" {
				t.Fatal("wrong acquisition provenance")
			}
			assertRetainedHomepage(t, first, fixturePage("test-visitor", "original", visitorBound))
			proofs := first.Tokens()
			binding := "video-a"
			if visitorBound {
				binding = "test-visitor"
			}
			if proofs.GVSBinding != binding || proofs.VisitorData != first.Config().VisitorData || proofs.ClientVersion != first.Config().ClientVersion {
				t.Fatal("proof/configuration identity mismatch")
			}
			for token, want := range map[string]string{proofs.Player: "fixture:1:video-a", proofs.GVS: "fixture:2:" + binding} {
				raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(token, "="))
				if err != nil || string(raw) != want {
					t.Fatal("wrong synthetic proof binding")
				}
			}
			for range 3 {
				s, err := c.Snapshot(context.Background(), "video-a")
				if err != nil || s.Tokens() != proofs || string(s.Config().Context) != string(first.Config().Context) {
					t.Fatal("cached snapshot changed")
				}
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			f.checkCalls(t, 1, 1, 1, 0)
			if first.Config().VisitorData != "test-visitor" {
				t.Fatal("snapshot changed after Close")
			}
			assertRetainedHomepage(t, first, fixturePage("test-visitor", "original", visitorBound))
			s, err := c.Snapshot(context.Background(), "video-a")
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("expected ErrClosed, got %v", err)
			}
			assertZeroSnapshot(t, s)
		})
	}
}

func TestSnapshotImmutabilityAndConcurrency(t *testing.T) {
	f := newOfflineTransport(t, false)
	c := newOfflineClient(t, f)
	s, err := c.Snapshot(context.Background(), "video-a")
	if err != nil {
		t.Fatal(err)
	}
	want := string(s.Config().Context)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			cfg := s.Config()
			cfg.ClientVersion, cfg.VisitorData = "changed", "changed"
			for i := range cfg.Context {
				cfg.Context[i] = 'x'
			}
			proofs := s.Tokens()
			proofs.Player = "changed"
			next, err := c.Snapshot(context.Background(), "video-a")
			if err != nil || string(next.Config().Context) != want || next.Tokens() != s.Tokens() {
				t.Error("mutable/shared snapshot state")
			}
		})
	}
	wg.Wait()
	if string(s.Config().Context) != want {
		t.Fatal("returned JSON changed retained context")
	}
	f.checkCalls(t, 1, 1, 1, 0)
}

func TestSessionReplacementAndAtomicProvenance(t *testing.T) {
	for _, sameIdentity := range []bool{false, true} {
		t.Run(fmt.Sprint(sameIdentity), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
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
				origin, err := pc.TokensWithSession(context.Background(), "video-a")
				if err != nil {
					t.Fatal(err)
				}
				first, err := c.Snapshot(context.Background(), "video-a")
				if err != nil {
					t.Fatal(err)
				}
				visitor := "replacement-visitor"
				if sameIdentity {
					visitor = "test-visitor"
				}
				f.mu.Lock()
				f.page = fixturePage(visitor, "replacement-marker", true)
				f.redirect = true
				f.mu.Unlock()
				time.Sleep(time.Minute) // Virtual time: expire Potter's synthetic session.
				next, err := c.Snapshot(context.Background(), "video-b")
				if err != nil {
					t.Fatal(err)
				}
				if next.Generation() != 2 || next.DocumentURL() != "https://www.youtube.com/" || next.Tokens().GVSBinding != visitor || next.Config().VisitorData != visitor {
					t.Fatal("replacement did not use its own session provenance")
				}
				if !bytes.Contains(next.Config().Context, []byte("replacement-marker")) {
					t.Fatal("replacement lost new context")
				}
				assertRetainedHomepage(t, next, fixturePage(visitor, "replacement-marker", true))
				assertRetainedHomepage(t, first, fixturePage("test-visitor", "original", false))
				if sameIdentity && (first.Config().VisitorData != next.Config().VisitorData || first.Config().ClientVersion != next.Config().ClientVersion) {
					t.Fatal("fixture must preserve identity across distinct homepages")
				}
				// Parse an older retained Potter artifact AFTER publication of the
				// newer generation, even when their visitor/version tuples are equal.
				old, err := snapshotFromPotter(context.Background(), origin, "video-a")
				if err != nil {
					t.Fatal(err)
				}
				if old.Generation() != 1 || old.DocumentURL() != "https://m.youtube.com/" || old.Tokens() != first.Tokens() || string(old.Config().Context) != string(first.Config().Context) {
					t.Fatal("older origin was substituted with a newer generation")
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				if err := pc.Close(); err != nil {
					t.Fatal(err)
				}
				if string(first.Config().Context) != string(old.Config().Context) {
					t.Fatal("old snapshot changed after closure")
				}
				assertRetainedHomepage(t, first, fixturePage("test-visitor", "original", false))
				assertRetainedHomepage(t, old, fixturePage("test-visitor", "original", false))
				assertRetainedHomepage(t, next, fixturePage(visitor, "replacement-marker", true))
				f.checkCalls(t, 2, 1, 2, 1)
			})
		})
	}
}

func TestClientDependenciesAndLocalFailures(t *testing.T) {
	f := newOfflineTransport(t, false)
	pc, err := potter.NewClient(potter.WithHTTPClient(&http.Client{Transport: f}))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	for _, options := range [][]Option{
		{WithHTTPClient(nil)}, {WithPotterClient(nil)},
		{WithHTTPClient(nil), WithPotterClient(pc)},
		{WithPotterClient(pc), WithHTTPClient(nil)},
	} {
		if c, err := NewClient(options...); err == nil || c != nil {
			t.Fatal("invalid dependencies accepted")
		}
	}
	defaultClient, err := NewClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = defaultClient.Close() // Default construction/closure is also lazy.
	c := newOfflineClient(t, f)
	for _, call := range []struct {
		ctx   context.Context
		video string
	}{{nil, "video"}, {context.Background(), ""}} {
		s, err := c.Snapshot(call.ctx, call.video)
		if err == nil {
			t.Fatal("invalid arguments accepted")
		}
		assertZeroSnapshot(t, s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := c.Snapshot(ctx, "video")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation")
	}
	assertZeroSnapshot(t, s)
	s, err = snapshotFromPotter(context.Background(), potter.SessionSnapshot{}, "video")
	if !errors.Is(err, ErrHomepageConfig) {
		t.Fatal("empty Potter origin accepted")
	}
	assertZeroSnapshot(t, s)
	f.checkCalls(t, 0, 0, 0, 0)
}

func TestBorrowedPotterOwnership(t *testing.T) {
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
	if c.http != nil {
		t.Fatal("borrowed Potter must not silently select a downstream policy")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.TokensWithSession(context.Background(), "video"); err != nil {
		t.Fatalf("borrowed Potter was closed: %v", err)
	}
	f.checkCalls(t, 1, 1, 1, 0)
}

func TestHTTPPolicyComposition(t *testing.T) {
	for _, mode := range []string{"owned", "borrowed-http-first", "borrowed-potter-first"} {
		t.Run(mode, func(t *testing.T) {
			f := newOfflineTransport(t, false)
			f.redirect = true
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			redirects := 0
			hc := &http.Client{Transport: f, Timeout: 7 * time.Second, Jar: jar,
				CheckRedirect: func(*http.Request, []*http.Request) error { redirects++; return nil }}
			options := []Option{WithHTTPClient(hc)}
			var pc *potter.Client
			if mode != "owned" {
				// A distinct transport proves supplying ytx's HTTP policy does not
				// reconfigure the lender's Potter instance.
				pf := newOfflineTransport(t, false)
				pc, err = potter.NewClient(potter.WithHTTPClient(&http.Client{Transport: pf}))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pc.Close(); pf.checkCalls(t, 1, 1, 1, 0) })
				if mode == "borrowed-http-first" {
					options = append(options, WithPotterClient(pc))
				} else {
					options = []Option{WithPotterClient(pc), WithHTTPClient(hc)}
				}
			}
			c, err := NewClient(options...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if c.http == hc || c.http.Transport != f || c.http.Timeout != hc.Timeout || c.http.Jar != jar || c.http.CheckRedirect == nil {
				t.Fatal("HTTP policy was not shallow-copied")
			}
			if hc.Transport != f || hc.Timeout != 7*time.Second || hc.Jar != jar {
				t.Fatal("constructor mutated caller HTTP client")
			}
			// Reassigning caller configuration must not change either retained copy.
			hc.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("caller replacement transport must not be used")
			})
			hc.Timeout, hc.Jar, hc.CheckRedirect = time.Nanosecond, nil, nil
			if c.http.Transport != f || c.http.Timeout != 7*time.Second || c.http.Jar != jar || c.http.CheckRedirect == nil {
				t.Fatal("caller reassignment changed retained HTTP policy")
			}
			if _, err := c.Snapshot(context.Background(), "video"); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if pc != nil {
				if _, err := pc.TokensWithSession(context.Background(), "video"); err != nil {
					t.Fatalf("borrowed Potter was closed: %v", err)
				}
				f.checkCalls(t, 0, 0, 0, 0)
				if redirects != 0 {
					t.Fatal("borrowed Potter used ytx HTTP policy")
				}
			} else {
				f.checkCalls(t, 1, 1, 1, 1)
				if redirects != 1 {
					t.Fatal("owned Potter did not retain redirect policy")
				}
				if _, err := c.potter.TokensWithSession(context.Background(), "video"); !errors.Is(err, potter.ErrClosed) {
					t.Fatalf("owned Potter was not closed: %v", err)
				}
			}
		})
	}
}

func TestDefaultHTTPPolicy(t *testing.T) {
	f := newOfflineTransport(t, false)
	// Keep default construction offline while checking that both clients select
	// the same default transport. These tests do not run in parallel.
	previous := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = previous })
	c, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if !c.owned || c.http == nil || c.http.Transport != f || c.http.Timeout != 30*time.Second || c.http.Jar != nil || c.http.CheckRedirect != nil {
		t.Fatal("unexpected default HTTP policy")
	}
	f.checkCalls(t, 0, 0, 0, 0)
	if _, err := c.Snapshot(context.Background(), "video"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	f.checkCalls(t, 1, 1, 1, 0)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInFlightCancellationAndClose(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		for _, closeClient := range []bool{false, true} {
			t.Run(fmt.Sprintf("borrowed=%t/close=%t", borrowed, closeClient), func(t *testing.T) {
				entered := make(chan struct{})
				hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					close(entered)
					<-r.Context().Done()
					return nil, r.Context().Err()
				})}
				option := WithHTTPClient(hc)
				if borrowed {
					pc, err := potter.NewClient(potter.WithHTTPClient(hc))
					if err != nil {
						t.Fatal(err)
					}
					defer pc.Close()
					option = WithPotterClient(pc)
				}
				c, err := NewClient(option)
				if err != nil {
					t.Fatal(err)
				}
				closeYTX := func() {
					closed := make(chan struct{})
					go func() { _ = c.Close(); close(closed) }()
					select {
					case <-closed:
					case <-time.After(5 * time.Second):
						t.Fatal("timed out closing ytx")
					}
				}
				t.Cleanup(closeYTX)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type result struct {
					snapshot SessionSnapshot
					err      error
				}
				done := make(chan result, 1)
				go func() { s, err := c.Snapshot(ctx, "video"); done <- result{s, err} }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for HTTP request")
				}
				if closeClient {
					closeYTX()
				} else {
					cancel()
				}
				select {
				case got := <-done:
					assertZeroSnapshot(t, got.snapshot)
					if !errors.Is(got.err, context.Canceled) && !(closeClient && errors.Is(got.err, potter.ErrClosed)) {
						t.Fatalf("unexpected cancellation result: %v", got.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for Snapshot cancellation")
				}
			})
		}
	}
}

func TestSnapshotFormattingAndZeroValue(t *testing.T) {
	assertZeroSnapshot(t, SessionSnapshot{})
	s := SessionSnapshot{tokens: potter.Tokens{Player: "SECRET"}, config: HomepageConfig{Context: json.RawMessage(`{"SECRET":true}`)}}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(format, s), "SECRET") || strings.Contains(fmt.Sprintf(format, s.Config()), "SECRET") {
			t.Fatal("diagnostic output leaked session data")
		}
	}
}

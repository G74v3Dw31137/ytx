package ytx

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRemediationSTS(t *testing.T) {
	for _, line := range []string{"\u2028", "\u2029", "\n", "\r", "\r\n"} {
		prefix := "/x/" + line + "if(1)/,sts:11111,/;"
		if n, e := extractSTS([]byte(prefix + "x={signatureTimestamp:20760};/x/")); e != nil || n != 20760 {
			t.Fatalf("line %q: %d %v", line, n, e)
		}
		if _, e := extractSTS([]byte(prefix)); e == nil {
			t.Fatal("fake candidate accepted")
		}
		f := newP2Transport(t)
		f.script = []byte(prefix + "x={signatureTimestamp:20760};/x/")
		c := newP2Client(t, f)
		if _, e := c.GetVideo(context.Background(), "aaaaaaaaaaa"); e != nil {
			t.Fatal(e)
		}
		if !bytes.Contains(f.bodies[1], []byte(`"signatureTimestamp":20760`)) {
			t.Fatal("wrong Player STS")
		}
	}
	for _, suffix := range []string{"function f(", "]", "x={sts:20760,", "([)]", "({", `"unterminated`, `/* unfinished`, "/unterminated", "`unfinished", "`${(}`", "/x/invalid", "/x/gg", "/x/v", "/x/é", "/x/\\u0067"} {
		if _, e := extractSTS([]byte("x={sts:20760};" + suffix)); e == nil {
			t.Fatalf("accepted %q", suffix)
		}
	}
	for _, suffix := range []string{"function f(){return ([{}]);}", "`hello ${([{}])}`", "/x/dgimsuy;", "var é=1;", "var x={stsé:1};"} {
		if n, e := extractSTS([]byte("x={sts:20760};" + suffix)); e != nil || n != 20760 {
			t.Fatalf("%s: %d %v", suffix, n, e)
		}
	}
}

func TestRemediationHistoricalFull(t *testing.T) {
	path := os.Getenv("YTX_P22_HISTORICAL_JS")
	if path == "" {
		t.Skip("set YTX_P22_HISTORICAL_JS to the archived sanitized gzip")
	}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	r, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	b, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	n, e := extractSTS(b)
	t.Logf("size=%d sha256=%x STS=%d error=%v", len(b), sha256.Sum256(b), n, e)
	if len(b) != 1548799 || n != 20760 || e != nil {
		t.Fail()
	}
}

func TestRemediationHTTPProvenance(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, metadata := range []string{"missing", "nil-url", "correct", "contradictory"} {
			for _, redirect := range []bool{false, true} {
				t.Run(method+metadata+map[bool]string{true: "redirect", false: "direct"}[redirect], func(t *testing.T) {
					var closed, reads atomic.Int32
					calls := 0
					target := "https://www.youtube.com/s/player/id/base.js"
					op := "player script"
					if method == "POST" {
						target = playerEndpoint
						op = "player"
					}
					c := &Client{http: &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
						calls++
						status := 200
						h := make(http.Header)
						if redirect && calls == 1 {
							status = 302
							h.Set("Location", "https://www.youtube.com/s/player/final/base.js")
						}
						resp := &http.Response{StatusCode: status, Header: h, Body: &trackedMetadataBody{Reader: strings.NewReader("ok"), closed: &closed, reads: &reads}}
						switch metadata {
						case "nil-url":
							resp.Request = &http.Request{}
						case "correct":
							resp.Request = r
						case "contradictory":
							bad := r.Clone(r.Context())
							u := *r.URL
							u.Path = "/s/player/other/base.js"
							bad.URL = &u
							resp.Request = bad
						}
						return resp, nil
					})}}
					_, final, e := c.readMetadata(context.Background(), op, method, target, nil)
					reject := metadata == "contradictory" || method == "POST" && redirect
					if (e != nil) != reject {
						t.Fatalf("error=%v", e)
					}
					if !reject {
						want := target
						if redirect {
							want = "https://www.youtube.com/s/player/final/base.js"
						}
						if final != want {
							t.Fatalf("final=%s", final)
						}
					}
					if int(closed.Load()) != calls {
						t.Fatalf("bodies closed=%d calls=%d", closed.Load(), calls)
					}
				})
			}
		}
	}
}

func TestRemediationRequestBudgets(t *testing.T) {
	for _, n := range []int{3, 8} {
		f := newP2Transport(t)
		c := newP2Client(t, f)
		var wg sync.WaitGroup
		var videosMu sync.Mutex
		var videos []*Video
		call := func() {
			v, e := c.GetVideo(context.Background(), "aaaaaaaaaaa")
			if e != nil {
				t.Error(e)
				return
			}
			videosMu.Lock()
			videos = append(videos, v)
			videosMu.Unlock()
		}
		if n == 8 {
			for range n {
				wg.Go(call)
			}
			wg.Wait()
		} else {
			for range n {
				call()
			}
		}
		scripts, posts := 0, 0
		for _, r := range f.requests {
			if r.Method == "GET" {
				scripts++
			} else {
				posts++
			}
		}
		t.Logf("calls=%d JS GET=%d Player POST=%d", n, scripts, posts)
		if scripts != 1 || posts != n {
			t.Fatal("request budget")
		}
		for _, v := range videos {
			if &v.player.source[0] != &videos[0].player.source[0] {
				t.Fatal("unshared storage")
			}
		}
		c.Close()
		if len(videos[0].player.source) == 0 || len(videos[0].Formats()) != 3 {
			t.Fatal("retained video invalid")
		}
	}
}

func cacheTestClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{http: &http.Client{Transport: rt}, life: ctx, cancel: cancel}
	t.Cleanup(func() { c.Close() })
	return c
}

const cacheURL = "https://www.youtube.com/s/player/id/base.js"

func cacheResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`x={sts:20760};`)), Request: r}
}

func TestRemediationCacheCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			select {
			case <-release:
				return cacheResponse(r), nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}))
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, e := c.playerAsset(canceled, cacheURL); !errors.Is(e, context.Canceled) || calls.Load() != 0 {
			t.Fatal("prejoin cancellation")
		}
		a, ca := context.WithCancel(context.Background())
		b, cb := context.WithCancel(context.Background())
		defer cb()
		ea, eb := make(chan error, 1), make(chan error, 1)
		go func() { _, e := c.playerAsset(a, cacheURL); ea <- e }()
		synctest.Wait()
		go func() { _, e := c.playerAsset(b, cacheURL); eb <- e }()
		synctest.Wait()
		ca()
		synctest.Wait()
		if !errors.Is(<-ea, context.Canceled) || calls.Load() != 1 {
			t.Fatal("independent cancellation")
		}
		close(release)
		synctest.Wait()
		if e := <-eb; e != nil {
			t.Fatal(e)
		}
	})
	for _, closeClient := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() }))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 2)
			for range 2 {
				go func() { _, e := c.playerAsset(ctx, cacheURL); done <- e }()
			}
			synctest.Wait()
			if closeClient {
				c.Close()
			} else {
				cancel()
			}
			synctest.Wait()
			for range 2 {
				if e := <-done; e == nil {
					t.Fatal("missing cancellation")
				}
			}
			c.mu.Lock()
			if len(c.flights) != 0 || len(c.assets) != 0 {
				t.Fatal("abandoned work retained")
			}
			c.mu.Unlock()
		})
	}
}

func TestRemediationCacheBoundsAndRetry(t *testing.T) {
	var calls atomic.Int32
	var large atomic.Bool
	var fail atomic.Bool
	c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if fail.Swap(false) {
			return nil, errors.New("synthetic failure")
		}
		resp := cacheResponse(r)
		if large.Load() {
			resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(`x={sts:20760};/*`), strings.NewReader(strings.Repeat("x", 6<<20)), strings.NewReader("*/")))
		}
		return resp, nil
	}))
	fail.Store(true)
	if _, e := c.playerAsset(context.Background(), cacheURL); e == nil {
		t.Fatal("failure cached as success")
	}
	first, e := c.playerAsset(context.Background(), cacheURL)
	if e != nil {
		t.Fatal(e)
	}
	for i := range maxPlayerAssets {
		if _, e := c.playerAsset(context.Background(), strings.Replace(cacheURL, "/id/", "/v"+string(rune('a'+i))+"/", 1)); e != nil {
			t.Fatal(e)
		}
	}
	if len(c.assets) != maxPlayerAssets {
		t.Fatal("entry bound")
	}
	n := calls.Load()
	if _, e := c.playerAsset(context.Background(), cacheURL); e != nil || calls.Load() != n+1 {
		t.Fatal("eviction did not reacquire")
	}
	if string(first.source) != `x={sts:20760};` {
		t.Fatal("evicted asset mutated")
	}
	large.Store(true)
	for _, id := range []string{"large1", "large2", "large3"} {
		if _, e := c.playerAsset(context.Background(), strings.Replace(cacheURL, "/id/", "/"+id+"/", 1)); e != nil {
			t.Fatal(e)
		}
	}
	if c.assetBytes > maxPlayerAssetBytes || len(c.assets) != 2 {
		t.Fatalf("byte bound: bytes=%d entries=%d", c.assetBytes, len(c.assets))
	}
	c.Close()
	if _, e := c.playerAsset(context.Background(), cacheURL); !errors.Is(e, ErrClosed) {
		t.Fatal("post-close work admitted")
	}
	if string(first.source) != `x={sts:20760};` {
		t.Fatal("close invalidated retained asset")
	}
}

func TestRemediationFlightAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, maxPlayerFlights+2)
		for i := range maxPlayerFlights + 2 {
			go func() {
				_, e := c.playerAsset(ctx, strings.Replace(cacheURL, "/id/", "/"+string(rune('a'+i))+"/", 1))
				done <- e
			}()
		}
		synctest.Wait()
		if calls.Load() != maxPlayerFlights || len(c.flights) != maxPlayerFlights {
			t.Fatal("in-flight bound")
		}
		c.Close()
		synctest.Wait()
		for range maxPlayerFlights + 2 {
			if e := <-done; e == nil {
				t.Fatal("close did not cancel admission wait")
			}
		}
		if len(c.flights) != 0 {
			t.Fatal("workers not joined")
		}
	})
}

func TestRemediationLastWaiterRetryAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return cacheResponse(r), nil
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := c.playerAsset(ctx, cacheURL); done <- e }()
			synctest.Wait()
			if !timeout {
				cancel()
			} // With all goroutines blocked synctest advances to the work deadline.
			if e := <-done; e == nil {
				t.Fatal("missing cancellation/deadline")
			}
			synctest.Wait()
			if len(c.assets) != 0 {
				t.Fatal("canceled flight published")
			}
			if _, e := c.playerAsset(context.Background(), cacheURL); e != nil || calls.Load() != 2 {
				t.Fatalf("retry: %v", e)
			}
		})
	}
}

func TestRemediationRedirectMutation(t *testing.T) {
	var calls atomic.Int32
	c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		resp := cacheResponse(r)
		resp.StatusCode = 302
		resp.Header.Set("Location", "/s/player/final/base.js")
		return resp, nil
	}))
	c.http.CheckRedirect = func(r *http.Request, _ []*http.Request) error { r.URL.Host = "evil.invalid"; return nil }
	if _, _, e := c.readMetadata(context.Background(), "player script", "GET", cacheURL, nil); e == nil || calls.Load() != 1 {
		t.Fatal("redirect callback bypassed destination validation")
	}
}

func TestRemediationSameAssetAcrossSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newP2Transport(t)
		c := newP2Client(t, f)
		first, e := c.GetVideo(context.Background(), "aaaaaaaaaaa")
		if e != nil {
			t.Fatal(e)
		}
		f.base.mu.Lock()
		f.base.page = fixturePage("new-visitor", "new-marker", true)
		f.base.page = bytes.ReplaceAll(f.base.page, []byte("/s/player/new-marker/base.js"), []byte("/s/player/original/base.js"))
		f.base.mu.Unlock()
		time.Sleep(time.Minute)
		next, e := c.GetVideo(context.Background(), "aaaaaaaaaaa")
		if e != nil {
			t.Fatal(e)
		}
		if next.snapshot.Generation() != 2 || next.snapshot.tokens.VisitorData != "new-visitor" || next.player.url != first.player.url || &next.player.source[0] != &first.player.source[0] {
			t.Fatal("same URL lost session association/reuse")
		}
		if len(f.requests) != 3 {
			t.Fatalf("requests=%d", len(f.requests))
		}
		checkPlayerRequest(t, f.requests[2], f.bodies[2], next.snapshot, "aaaaaaaaaaa")
	})
}

func TestRemediationAbandonedWorkCannotPublish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-release
			return cacheResponse(r), nil
		}))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, e := c.playerAsset(ctx, cacheURL); done <- e }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if !errors.Is(<-done, context.Canceled) {
			t.Fatal("waiter did not cancel")
		}
		retry := make(chan error, 1)
		go func() { _, e := c.playerAsset(context.Background(), cacheURL); retry <- e }()
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("abandoned same-key work duplicated before exit")
		}
		close(release)
		synctest.Wait()
		if e := <-retry; e != nil || calls.Load() != 2 || len(c.assets) != 1 {
			t.Fatalf("retry: %v calls=%d", e, calls.Load())
		}
	})
}

func TestRemediationSharedFailureAndRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		cause := errors.New("synthetic shared failure")
		c := cacheTestClient(t, metadataTransport(func(r *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				<-release
				return nil, cause
			}
			return cacheResponse(r), nil
		}))
		done := make(chan error, 3)
		for range 3 {
			go func() { _, e := c.playerAsset(context.Background(), cacheURL); done <- e }()
		}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("failure flight duplicated")
		}
		close(release)
		synctest.Wait()
		for range 3 {
			if e := <-done; !errors.Is(e, cause) {
				t.Fatalf("shared error=%v", e)
			}
		}
		if len(c.assets) != 0 {
			t.Fatal("failed work retained")
		}
		if _, e := c.playerAsset(context.Background(), cacheURL); e != nil || calls.Load() != 2 {
			t.Fatalf("retry=%v", e)
		}
	})
}

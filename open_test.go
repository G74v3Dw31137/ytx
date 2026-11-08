package ytx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type delayedOpenBody struct {
	io.ReadCloser
	ctx context.Context
}

func (b *delayedOpenBody) Read(p []byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-time.After(31 * time.Second):
		return b.ReadCloser.Read(p)
	}
}

func TestOpenTimeoutPolicy(t *testing.T) {
	for _, policy := range []string{"default", "explicit timeout", "explicit no timeout"} {
		t.Run(policy, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
					body := io.NopCloser(strings.NewReader("media"))
					if req.URL.Host == "rr1---sn-test.googlevideo.com" {
						body = &delayedOpenBody{ReadCloser: body, ctx: req.Context()}
					} else if _, ok := req.Context().Deadline(); !ok {
						t.Error("metadata request lost its timeout")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}, nil
				})
				var options []Option
				if policy == "default" {
					previous := http.DefaultTransport
					http.DefaultTransport = transport
					defer func() { http.DefaultTransport = previous }()
				} else {
					timeout := time.Duration(0)
					if policy == "explicit timeout" {
						timeout = 30 * time.Second
					}
					options = append(options, WithHTTPClient(&http.Client{Transport: transport, Timeout: timeout}))
				}
				c, err := NewClient(options...)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				v := resolveVideo(t, c, resolveAsset(t, ""), Format{URL: mediaBase})
				stream, err := c.Open(context.Background(), v, v.Formats()[0])
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				data, err := io.ReadAll(stream)
				if policy == "explicit timeout" {
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("explicit timeout: read error = %v", err)
					}
				} else if err != nil || string(data) != "media" {
					t.Fatalf("stream after 30 seconds = %q, error = %v", data, err)
				}
				if policy == "default" {
					if c.http.Timeout != 30*time.Second {
						t.Fatal("default metadata timeout changed")
					}
					_, _, err := c.readMetadata(context.Background(), "player script", http.MethodGet, "https://www.youtube.com/s/player/fixture/base.js", nil)
					if err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
}

type openTestBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *openTestBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

func openTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *Video, **openTestBody) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	local, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var body *openTestBody
	c, err := NewClient(WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Only the configured transport reaches the local test server. Resolve's
		// URL validation and transformations run unchanged, without live requests.
		if req.Method != http.MethodGet || req.URL.Host != "rr1---sn-test.googlevideo.com" || req.URL.Path != "/videoplayback" {
			return nil, errors.New("unexpected media request")
		}
		if q := req.URL.Query(); q.Get("pot") != "origin+GVS/=" || q.Get("n") != "ok_abc" || q.Get("signature") != "cba" {
			t.Error("Open did not use the resolved URL")
		}
		if req.Header.Get("Range") != "" {
			t.Error("unexpected Range header")
		}
		rewritten := req.Clone(req.Context())
		rewritten.URL.Scheme, rewritten.URL.Host = local.Scheme, local.Host
		resp, err := server.Client().Transport.RoundTrip(rewritten)
		if err == nil {
			body = &openTestBody{ReadCloser: resp.Body}
			resp.Body = body
		}
		return resp, err
	})}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	v := resolveVideo(t, c, resolveAsset(t, ""), cipherFormat(mediaBase+"?n=abc", "abc", ""))
	return c, v, &body
}

func TestOpenStreaming(t *testing.T) {
	release := make(chan struct{})
	c, v, responseBody := openTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			io.WriteString(w, "second")
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := c.Open(ctx, v, v.Formats()[0])
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if stream != *responseBody {
		t.Fatal("Open did not return resp.Body directly")
	}
	first := make([]byte, len("first"))
	if _, err := io.ReadFull(stream, first); err != nil || string(first) != "first" {
		t.Fatalf("first chunk = %q, error = %v", first, err)
	}
	close(release)
	rest, err := io.ReadAll(stream)
	if err != nil || string(rest) != "second" {
		t.Fatalf("remaining stream = %q, error = %v", rest, err)
	}
	if (*responseBody).closed.Load() {
		t.Fatal("Open closed the caller's stream")
	}
	if err := stream.Close(); err != nil || !(*responseBody).closed.Load() {
		t.Fatalf("caller close failed: %v", err)
	}
}

func TestOpenHTTPError(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusPartialContent, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, v, responseBody := openTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := c.Open(ctx, v, v.Formats()[0])
			var httpErr *HTTPError
			if stream != nil || !errors.As(err, &httpErr) || httpErr.StatusCode != status || httpErr.Operation != "open" {
				t.Fatalf("stream = %v, error = %v", stream, err)
			}
			if !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Fatalf("error omits status: %v", err)
			}
			if *responseBody == nil || !(*responseBody).closed.Load() {
				t.Fatal("HTTP error response body was not closed")
			}
		})
	}
}

func TestOpenCancellation(t *testing.T) {
	for _, afterHeaders := range []bool{false, true} {
		t.Run(fmt.Sprintf("afterHeaders=%t", afterHeaders), func(t *testing.T) {
			started := make(chan struct{})
			c, v, _ := openTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if afterHeaders {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if afterHeaders {
				stream, err := c.Open(ctx, v, v.Formats()[0])
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				cancel()
				if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
					t.Fatalf("read error = %v", err)
				}
			} else {
				go func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				}()
				stream, err := c.Open(ctx, v, v.Formats()[0])
				if stream != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("stream = %v, error = %v", stream, err)
				}
			}
		})
	}
}

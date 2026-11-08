// Package ytx retrieves YouTube video metadata and formats, resolves media URLs,
// and opens sequential media streams.
package ytx

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/G74v3Dw31137/potter"
	"github.com/G74v3Dw31137/ytx/internal/player"
	"github.com/dop251/goja"
)

// ErrClosed indicates that the ytx client has been closed.
var ErrClosed = errors.New("ytx: client closed")

// Option configures a client before use.
type Option func(*clientConfig)

type clientConfig struct {
	http      *http.Client
	httpSet   bool
	potter    *potter.Client
	potterSet bool
}

// WithHTTPClient supplies the anonymous HTTP policy for ytx's downstream
// requests and, when owned, Potter. Its configuration is shallow-copied; transport
// and other referenced policy objects remain shared and caller-owned. Requests
// must honor contexts. A borrowed Potter client is never reconfigured.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *clientConfig) { c.http, c.httpSet = hc, true }
}

// WithPotterClient borrows an existing, constructed and open Potter client. Closing ytx
// cancels its own calls but does not close the borrowed client or its shared
// builders. The caller must keep it open while using ytx and close it afterward.
// Without WithHTTPClient, only snapshot operations are configured: no downstream
// HTTP policy is selected. Potter's private policy cannot be recovered. Supply a
// compatible anonymous policy explicitly for GetVideo.
func WithPotterClient(pc *potter.Client) Option {
	return func(c *clientConfig) { c.potter, c.potterSet = pc, true }
}

// Client delegates session and token management to Potter. Construct it with
// NewClient and close it after use. Methods may be called concurrently; the zero
// value is not usable.
type Client struct {
	potter       *potter.Client
	http         *http.Client // nil for borrowed Potter without explicit downstream policy
	defaultHTTP  bool         // Open omits the default total request timeout
	owned        bool
	life         context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closed       bool
	active       sync.WaitGroup
	closeOnce    sync.Once
	assets       []playerAsset
	assetBytes   int
	flights      map[string]*assetFlight
	assetChanged chan struct{}
	assetWorkers sync.WaitGroup
	// Private execution boundaries are fixed before use (also used by offline tests).
	prepareCode       func(context.Context, []byte) (*goja.Program, error)
	initializeRuntime func(context.Context, *goja.Program) (*player.Session, error)
	identity          *ownerToken
	preparing         *prepareFlight
	prepareChanged    chan struct{}
	prepareWorkers    sync.WaitGroup
	resolveSlots      chan struct{}
}

// NewClient constructs a client without making network requests. By default it
// owns a lazily initialized Potter client. Both clients use a 30-second HTTP
// timeout and the current http.DefaultTransport unless WithHTTPClient is supplied.
// Open omits this default total timeout for media streaming.
func NewClient(options ...Option) (*Client, error) {
	var cfg clientConfig
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	if cfg.httpSet && cfg.http == nil || cfg.potterSet && cfg.potter == nil {
		return nil, errors.New("ytx: nil client dependency")
	}
	pc := cfg.potter
	var hc *http.Client
	if cfg.httpSet {
		policy := *cfg.http
		hc = &policy
	} else if !cfg.potterSet {
		hc = &http.Client{Transport: http.DefaultTransport, Timeout: 30 * time.Second}
	}
	if !cfg.potterSet {
		var err error
		pc, err = potter.NewClient(potter.WithHTTPClient(hc))
		if err != nil {
			return nil, err
		}
	}
	life, cancel := context.WithCancel(context.Background())
	return &Client{potter: pc, http: hc, defaultHTTP: !cfg.httpSet, owned: !cfg.potterSet, life: life, cancel: cancel, identity: &ownerToken{}, resolveSlots: make(chan struct{}, 2)}, nil
}

// Snapshot acquires proofs and extracts configuration from their exact Potter
// session's homepage. It adds no homepage or other HTTP requests of its own.
// videoID must be nonempty and ctx must be non-nil. Errors return a zero snapshot;
// cancellation remains observable with errors.Is. Potter controls acquisition,
// refresh, binding, and caching; no remote token validity or lifetime is promised.
func (c *Client) Snapshot(ctx context.Context, videoID string) (SessionSnapshot, error) {
	work, done, err := c.begin(ctx, videoID)
	if err != nil {
		return SessionSnapshot{}, err
	}
	defer done()
	return c.snapshot(work, videoID)
}

func (c *Client) begin(ctx context.Context, videoID string) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("ytx: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if videoID == "" {
		return nil, nil, errors.New("ytx: empty video ID")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClosed
	}
	c.active.Add(1)
	c.mu.Unlock()
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.life, cancel)
	done := func() { stop(); cancel(); c.active.Done() }
	if c.life.Err() != nil {
		done()
		return nil, nil, ErrClosed
	}
	return work, done, nil
}

func (c *Client) snapshot(ctx context.Context, videoID string) (SessionSnapshot, error) {
	origin, err := c.potter.TokensWithSession(ctx, videoID)
	if err != nil {
		return SessionSnapshot{}, err
	}
	return snapshotFromPotter(ctx, origin, videoID)
}

// Close cancels and joins ytx calls and closes an owned Potter client. It is
// idempotent. It never closes a borrowed Potter client or a caller's transport.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.cancel()
		c.mu.Unlock()
		if c.owned {
			_ = c.potter.Close()
		}
		c.active.Wait()
		c.assetWorkers.Wait()
		c.prepareWorkers.Wait()
		c.mu.Lock()
		c.assets = nil
		c.assetBytes = 0
		c.mu.Unlock()
	})
	return nil
}

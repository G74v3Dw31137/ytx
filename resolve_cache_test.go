package ytx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/G74v3Dw31137/ytx/internal/player"
	"github.com/dop251/goja"
)

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("synchronization condition not reached")
		}
		runtime.Gosched()
	}
}
func waiterCount(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.preparing == nil {
		return 0
	}
	return c.preparing.waiters
}
func TestPreparationIdentityAndResidency(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	var count atomic.Int32
	c.prepareCode = func(ctx context.Context, b []byte) (*goja.Program, error) {
		count.Add(1)
		return player.Compile(ctx, b)
	}
	p, e := c.preparedPlayer(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	alias := a
	alias.url = "https://www.youtube.com/s/player/other/base.js"
	p2, e := c.preparedPlayer(context.Background(), alias)
	if e != nil || p2 != p || count.Load() != 1 {
		t.Fatal("same digest not shared")
	}
	changed := resolveAsset(t, string(a.source)+"\n// changed bytes")
	p2, e = c.preparedPlayer(context.Background(), changed)
	if e != nil || p2 == p || count.Load() != 2 {
		t.Fatal("different bytes shared")
	}
	old := resolveVideo(t, c, a, Format{URL: mediaBase + "?n=old"})
	for i := range maxPlayerAssets + 1 {
		next := resolveAsset(t, string(a.source)+fmt.Sprintf("\n// %d", i))
		if _, e = c.preparedPlayer(context.Background(), next); e != nil {
			t.Fatal(e)
		}
	}
	c.mu.Lock()
	if len(c.assets) > maxPlayerAssets || c.assetBytes > maxPlayerAssetBytes {
		t.Fatal("unbounded registry")
	}
	for _, r := range c.assets {
		if r.sha256 == a.sha256 {
			t.Fatal("old asset not evicted")
		}
	}
	c.mu.Unlock()
	before := count.Load()
	if _, e = c.Resolve(context.Background(), old, old.Formats()[0]); e != nil {
		t.Fatal(e)
	}
	if count.Load() != before+1 {
		t.Fatal("old retained source not readmitted")
	}
}
func TestConcurrentPreparationAndWaiterCancellation(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c.prepareCode = func(ctx context.Context, b []byte) (*goja.Program, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return player.Compile(ctx, b)
	}
	type result struct {
		p *goja.Program
		e error
	}
	results := make(chan result, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { p, e := c.preparedPlayer(ctx, a); results <- result{p, e} }()
	<-started
	for range 9 {
		go func() { p, e := c.preparedPlayer(context.Background(), a); results <- result{p, e} }()
	}
	await(t, func() bool { return waiterCount(c) == 10 })
	cancel()
	r := <-results
	if !errors.Is(r.e, context.Canceled) {
		t.Fatal(r.e)
	}
	await(t, func() bool { return waiterCount(c) == 9 })
	close(release)
	var p *goja.Program
	for range 9 {
		r := <-results
		if r.e != nil {
			t.Fatal(r.e)
		}
		if p != nil && p != r.p {
			t.Fatal("different compiled programs")
		}
		p = r.p
	}
	if calls.Load() != 1 {
		t.Fatal("preparation not coalesced")
	}
}
func TestAllPreparationWaitersCancelWithoutReleasingWorker(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c.prepareCode = func(ctx context.Context, b []byte) (*goja.Program, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
		}
		return player.Compile(ctx, b)
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { _, e := c.preparedPlayer(ctx, a); returned <- e }()
	<-started
	cancel()
	if e := <-returned; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	<-canceled
	// Synchronous work is deliberately still running after all waiters leave.
	c.mu.Lock()
	f := c.preparing
	if f == nil || f.waiters != 0 {
		t.Fatal("worker capacity released early")
	}
	c.mu.Unlock()
	retry := make(chan error, 1)
	observed := &observedPreparationContext{Context: context.Background(), reached: make(chan struct{})}
	go func() { _, e := c.preparedPlayer(observed, a); retry <- e }()
	<-observed.reached
	if calls.Load() != 1 {
		t.Fatal("new worker entered before abandoned worker exited")
	}
	close(release)
	if e := <-retry; e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal("cancellation cached as incompatibility")
	}
}
func TestPreparationNegativeCacheAndRuntimeFailure(t *testing.T) {
	c := resolveClient(t)
	var count atomic.Int32
	c.prepareCode = func(ctx context.Context, b []byte) (*goja.Program, error) {
		count.Add(1)
		return player.Compile(ctx, b)
	}
	bad := resolveAsset(t, "invalid js(")
	for range 3 {
		if _, e := c.preparedPlayer(context.Background(), bad); !errors.Is(e, ErrUnsupportedPlayerJS) {
			t.Fatal(e)
		}
	}
	if count.Load() != 1 {
		t.Fatal("negative result not cached")
	}
	good := resolveAsset(t, "")
	v := resolveVideo(t, c, good, Format{URL: mediaBase + "?n=throw"}, Format{URL: mediaBase + "?n=valid"})
	fs := v.Formats()
	if _, e := c.Resolve(context.Background(), v, fs[0]); !errors.Is(e, ErrN) {
		t.Fatal(e)
	}
	if _, e := c.Resolve(context.Background(), v, fs[1]); e != nil {
		t.Fatal(e)
	}
	if count.Load() != 2 {
		t.Fatal("runtime failure invalidated preparation")
	}
	// Compiler failure is a separate stable category.
	c2 := resolveClient(t)
	c2.prepareCode = func(context.Context, []byte) (*goja.Program, error) { return nil, player.ErrPreparation }
	if _, e := c2.preparedPlayer(context.Background(), good); !errors.Is(e, ErrPreparation) {
		t.Fatal(e)
	}
}
func TestRuntimeAdmissionAndCanceledWaiter(t *testing.T) {
	c := resolveClient(t)
	v := resolveVideo(t, c, resolveAsset(t, ""), Format{URL: mediaBase + "?n=a"})
	f := v.Formats()[0]
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var active, peak atomic.Int32
	c.initializeRuntime = func(ctx context.Context, p *goja.Program) (*player.Session, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return player.NewSession(ctx, p)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, e := c.Resolve(context.Background(), v, f); e != nil {
				t.Error(e)
			}
		})
	}
	<-started
	<-started
	if active.Load() != 2 || peak.Load() != 2 {
		t.Fatal("runtime bound")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.Resolve(ctx, v, f); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if out, e := c.Resolve(ctx, v, f); out != "" || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if active.Load() != 2 {
		t.Fatal("canceled waiter released active capacity")
	}
	close(release)
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatal("too many runtimes")
	}
}
func TestEvictionDuringResolution(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	v := resolveVideo(t, c, a, Format{URL: mediaBase + "?n=a"})
	entered := make(chan struct{})
	release := make(chan struct{})
	c.initializeRuntime = func(ctx context.Context, p *goja.Program) (*player.Session, error) {
		close(entered)
		<-release
		return player.NewSession(ctx, p)
	}
	done := make(chan error, 1)
	go func() { _, e := c.Resolve(context.Background(), v, v.Formats()[0]); done <- e }()
	<-entered
	for i := range maxPlayerAssets {
		b := resolveAsset(t, string(a.source)+fmt.Sprintf("\n// eviction %d", i))
		if _, e := c.preparedPlayer(context.Background(), b); e != nil {
			t.Fatal(e)
		}
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestCloseJoinsPreparation(t *testing.T) {
	c := resolveClient(t)
	v := resolveVideo(t, c, resolveAsset(t, ""), Format{URL: mediaBase + "?n=a"})
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	c.prepareCode = func(ctx context.Context, b []byte) (*goja.Program, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, e := c.Resolve(context.Background(), v, v.Formats()[0]); done <- e }()
	<-started
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	<-canceled
	select {
	case <-closed:
		t.Fatal("Close abandoned synchronous worker")
	default:
	}
	close(release)
	<-closed
	if e := <-done; !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.assets != nil || c.preparing != nil {
		t.Fatal("Close retained prepared cache")
	}
}
func TestCloseDuringRuntimeAndFreshRetry(t *testing.T) {
	c := resolveClient(t)
	v := resolveVideo(t, c, resolveAsset(t, ""), Format{URL: mediaBase + "?n=hang"})
	entered := make(chan struct{})
	c.initializeRuntime = func(ctx context.Context, p *goja.Program) (*player.Session, error) {
		s, e := player.NewSession(ctx, p)
		close(entered)
		return s, e
	}
	done := make(chan error, 1)
	go func() { _, e := c.Resolve(context.Background(), v, v.Formats()[0]); done <- e }()
	<-entered
	c.Close()
	if e := <-done; !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	if len(c.resolveSlots) != 0 {
		t.Fatal("runtime slot leaked")
	}
}
func TestUnsupportedPlayerAndSignatureFailure(t *testing.T) {
	c := resolveClient(t)
	if _, e := resolveOne(t, c, cipherFormat(mediaBase, "abc", "")); e != nil {
		t.Fatal(e)
	}
	a := resolveAsset(t, "")
	a = resolveAsset(t, strings.Replace(string(a.source), `this.values.s=encodeURIComponent(decodeURIComponent(this.values.s).split("").reverse().join(""))`, `this.values.s=null`, 1))
	v := resolveVideo(t, c, a, cipherFormat(mediaBase, "abc", ""))
	if out, e := c.Resolve(context.Background(), v, v.Formats()[0]); e == nil || out != "" {
		t.Fatal("required signature accepted invalid output")
	}
}

func TestDistinctPreparationAdmission(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	started := make(chan struct{})
	release := make(chan struct{})
	var active, peak, calls atomic.Int32
	c.prepareCode = func(ctx context.Context, b []byte) (*goja.Program, error) {
		n := active.Add(1)
		defer active.Add(-1)
		if n > peak.Load() {
			peak.Store(n)
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return player.Compile(ctx, b)
	}
	first := make(chan error, 1)
	go func() { _, e := c.preparedPlayer(context.Background(), a); first <- e }()
	<-started
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			different := resolveAsset(t, string(a.source)+fmt.Sprintf("\n// distinct %d", i))
			if _, e := c.preparedPlayer(context.Background(), different); e != nil {
				t.Error(e)
			}
		})
	}
	if active.Load() != 1 {
		t.Fatal("preparation not admitted")
	}
	close(release)
	if e := <-first; e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	if peak.Load() != 1 || calls.Load() != 9 {
		t.Fatal("preparation bound violated")
	}
}

// Done is observed only when the request reaches the abandoned-flight select.
type observedPreparationContext struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

func (c *observedPreparationContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.Context.Done()
}

func TestSameDigestReadmissionDuringPreparation(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c.prepareCode = func(ctx context.Context, b []byte) (*goja.Program, error) {
		calls.Add(1)
		close(started)
		<-release
		return player.Compile(ctx, b)
	}
	type result struct {
		p *goja.Program
		e error
	}
	done := make(chan result, 2)
	go func() { p, e := c.preparedPlayer(context.Background(), a); done <- result{p, e} }()
	<-started
	c.mu.Lock()
	for i := range maxPlayerAssets {
		b := resolveAsset(t, string(a.source)+fmt.Sprintf("\n// blocked %d", i))
		c.admitAsset(b)
	}
	for _, r := range c.assets {
		if r.sha256 == a.sha256 {
			t.Fatal("not evicted")
		}
	}
	c.mu.Unlock()
	go func() { p, e := c.preparedPlayer(context.Background(), a); done <- result{p, e} }()
	await(t, func() bool { return waiterCount(c) == 2 })
	c.mu.Lock()
	var slot *preparation
	for _, r := range c.assets {
		if r.sha256 == a.sha256 {
			slot = r.prepared
		}
	}
	if slot != c.preparing.slot {
		t.Fatal("readmission lost flight slot")
	}
	c.mu.Unlock()
	close(release)
	x, y := <-done, <-done
	if x.e != nil || y.e != nil || x.p != y.p || calls.Load() != 1 {
		t.Fatal("flight not shared", x.e, y.e)
	}
	p, e := c.preparedPlayer(context.Background(), a)
	if e != nil || p != x.p || calls.Load() != 1 {
		t.Fatal("completion not published")
	}
}

func TestHistoricalReadmissionCannotPublishAcquisition(t *testing.T) {
	c := resolveClient(t)
	a := resolveAsset(t, "")
	var downloads atomic.Int32
	c.http.Transport = metadataTransport(func(r *http.Request) (*http.Response, error) {
		source := string(a.source)
		if downloads.Add(1) > 1 {
			source = strings.Replace(source, "20760", "20761", 1)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(source)), Request: r}, nil
	})
	acquired, e := c.playerAsset(context.Background(), a.url)
	if e != nil || acquired.sha256 != a.sha256 {
		t.Fatal("initial acquisition", e)
	}
	old := resolveVideo(t, c, acquired, Format{URL: mediaBase + "?n=old"})
	c.mu.Lock()
	for i := range maxPlayerAssets {
		b := resolveAsset(t, string(a.source)+fmt.Sprintf("\n// evict %d", i))
		b.url = fmt.Sprintf("https://www.youtube.com/s/player/evict%d/base.js", i)
		c.admitAcquiredAsset(b)
	}
	for _, r := range c.assets {
		if r.sha256 == a.sha256 {
			t.Fatal("A not evicted")
		}
	}
	c.mu.Unlock()
	b, e := c.playerAsset(context.Background(), a.url)
	if e != nil || b.sha256 == a.sha256 || b.sts != 20761 {
		t.Fatal("new acquisition", e)
	}
	if got, e := c.Resolve(context.Background(), old, old.Formats()[0]); e != nil || !strings.Contains(got, "n=ok_old") {
		t.Fatal("old Video failed", e)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				got, e := c.playerAsset(context.Background(), a.url)
				if e != nil || got.sha256 != b.sha256 || got.sts != b.sts || string(got.source) != string(b.source) {
					t.Error("stale acquisition", e)
				}
			}
		})
	}
	wg.Wait()
	if downloads.Load() != 2 {
		t.Fatal("unexpected reacquisition")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	authorities := 0
	for _, r := range c.assets {
		if r.url == a.url && r.acquisition {
			authorities++
		}
	}
	if authorities != 1 || len(c.assets) > maxPlayerAssets || c.assetBytes > maxPlayerAssetBytes {
		t.Fatal("invalid registry")
	}
}

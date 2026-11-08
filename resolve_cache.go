package ytx

import (
	"context"
	"errors"

	"github.com/G74v3Dw31137/ytx/internal/player"
	"github.com/dop251/goja"
)

type preparation struct {
	program *goja.Program
	err     error
	ready   bool
}
type prepareFlight struct {
	digest  string
	slot    *preparation
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	program *goja.Program
	err     error
}

// admitAsset runs under mu and shares preparation only by exact source digest.
// A linear scan is bounded by the existing four-entry registry.
func (c *Client) admitAsset(a playerAsset) playerAsset {
	for _, resident := range c.assets {
		if resident.sha256 == a.sha256 && resident.prepared != nil {
			a.prepared = resident.prepared
			break
		}
	}
	if c.preparing != nil && c.preparing.digest == a.sha256 {
		a.prepared = c.preparing.slot
	}
	if a.prepared == nil {
		a.prepared = &preparation{}
	}
	for len(c.assets) >= maxPlayerAssets || c.assetBytes+len(a.source) > maxPlayerAssetBytes {
		c.assetBytes -= len(c.assets[0].source)
		c.assets[0] = playerAsset{}
		c.assets = c.assets[1:]
	}
	c.assets = append(c.assets, a)
	c.assetBytes += len(a.source)
	return a
}

func (c *Client) preparedPlayer(ctx context.Context, a playerAsset) (*goja.Program, error) {
	if len(a.source) == 0 || len(a.source) > player.MaxSource {
		return nil, ErrUnsupportedPlayerJS
	}
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, ErrClosed
		}
		if c.prepareChanged == nil {
			c.prepareChanged = make(chan struct{})
		}
		var slot *preparation
		for i, r := range c.assets {
			if r.sha256 == a.sha256 {
				if r.prepared == nil {
					r.prepared = &preparation{}
					c.assets[i] = r
				}
				slot = r.prepared
				break
			}
		}
		if slot == nil {
			a.prepared = nil
			a.acquisition = false
			a = c.admitAsset(a)
			slot = a.prepared
		}
		if slot.ready {
			p, e := slot.program, slot.err
			c.mu.Unlock()
			return p, e
		}
		f := c.preparing
		if f != nil && (f.digest != a.sha256 || f.waiters == 0) {
			changed := c.prepareChanged
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
			}
			continue
		}
		if f == nil {
			work, cancel := context.WithTimeout(c.life, playerAssetTimeout)
			f = &prepareFlight{digest: a.sha256, slot: slot, done: make(chan struct{}), cancel: cancel}
			c.preparing = f
			c.prepareWorkers.Add(1)
			go c.preparePlayer(work, a.source, f)
		}
		f.waiters++
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			c.leavePreparation(f)
			return nil, ctx.Err()
		case <-f.done:
			c.leavePreparation(f)
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			return f.program, f.err
		}
	}
}
func (c *Client) leavePreparation(f *prepareFlight) {
	c.mu.Lock()
	f.waiters--
	if f.waiters == 0 {
		f.cancel()
	}
	c.mu.Unlock()
}
func (c *Client) preparePlayer(ctx context.Context, source []byte, f *prepareFlight) {
	defer c.prepareWorkers.Done()
	compile := c.prepareCode
	if compile == nil {
		compile = player.Compile
	}
	p, e := compile(ctx, source)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ce := ctx.Err(); ce != nil {
		p = nil
		e = ce
	}
	f.program, f.err = p, e
	// Only source-deterministic failures are cached. Canceled builders remain
	// admitted until synchronous parsing/compilation has actually returned.
	if !c.closed && f.waiters > 0 && (e == nil || errors.Is(e, player.ErrUnsupported) || errors.Is(e, player.ErrPreparation)) {
		f.slot.program, f.slot.err, f.slot.ready = p, e, true
	}
	f.cancel()
	c.preparing = nil
	close(f.done)
	close(c.prepareChanged)
	c.prepareChanged = make(chan struct{})
}

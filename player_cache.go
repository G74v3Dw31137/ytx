package ytx

import (
	"context"
	"net/url"
	"time"
)

const (
	maxPlayerAssets     = 4
	maxPlayerAssetBytes = 16 << 20
	maxPlayerFlights    = 4
	playerAssetTimeout  = 30 * time.Second
)

type assetFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	asset   playerAsset
	err     error
}

// All cache state uses Client.mu. The worker count includes abandoned flights
// until their transports actually return, so cancellation cannot bypass admission.
func (c *Client) playerAsset(ctx context.Context, target string) (playerAsset, error) {
	u, err := url.Parse(target)
	if err != nil || !validPlayerScriptURL(u) || u.String() != target {
		return playerAsset{}, ErrPlayerScript
	}
	for {
		if err := ctx.Err(); err != nil {
			return playerAsset{}, err
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return playerAsset{}, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return playerAsset{}, err
		}
		for i, a := range c.assets {
			if a.acquisition && a.url == target {
				copy(c.assets[i:], c.assets[i+1:])
				c.assets[len(c.assets)-1] = a
				c.mu.Unlock()
				return a, nil
			}
		}
		if c.flights == nil {
			c.flights = make(map[string]*assetFlight)
			c.assetChanged = make(chan struct{})
		}
		f := c.flights[target]
		if f != nil && f.waiters == 0 || f == nil && len(c.flights) >= maxPlayerFlights {
			changed := c.assetChanged
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return playerAsset{}, ctx.Err()
			case <-c.life.Done():
				return playerAsset{}, context.Canceled
			case <-changed:
			}
			continue
		}
		if f == nil {
			work, cancel := context.WithTimeout(c.life, playerAssetTimeout)
			f = &assetFlight{done: make(chan struct{}), cancel: cancel}
			c.flights[target] = f
			c.assetWorkers.Add(1) // Close and admission use the same mutex.
			go c.acquirePlayerAsset(work, target, f)
		}
		f.waiters++
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			c.leaveAsset(f)
			return playerAsset{}, ctx.Err()
		case <-c.life.Done():
			c.leaveAsset(f)
			return playerAsset{}, context.Canceled
		case <-f.done:
			c.leaveAsset(f)
			if err := ctx.Err(); err != nil {
				return playerAsset{}, err
			}
			if c.life.Err() != nil {
				return playerAsset{}, context.Canceled
			}
			return f.asset, f.err
		}
	}
}

func (c *Client) leaveAsset(f *assetFlight) {
	c.mu.Lock()
	f.waiters--
	if f.waiters == 0 {
		f.cancel()
	}
	c.mu.Unlock()
}

func (c *Client) acquirePlayerAsset(ctx context.Context, target string, f *assetFlight) {
	defer c.assetWorkers.Done()
	a, err := c.downloadPlayer(ctx, target)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		err = ctx.Err()
	}
	f.asset, f.err = a, err
	if err == nil && !c.closed && f.waiters > 0 {
		c.admitAcquiredAsset(a)
	}
	f.cancel()
	delete(c.flights, target)
	close(f.done)
	close(c.assetChanged)
	c.assetChanged = make(chan struct{})
}

// admitAcquiredAsset runs under mu. Only completed acquisition can publish URL authority.
func (c *Client) admitAcquiredAsset(a playerAsset) playerAsset {
	for i := range c.assets {
		if c.assets[i].url == a.url {
			c.assets[i].acquisition = false
		}
	}
	a.acquisition = true
	return c.admitAsset(a)
}

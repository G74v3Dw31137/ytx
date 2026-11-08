package ytx

import (
	"context"
	"io"
	"net/http"
)

// Open resolves format and opens a sequential, non-seekable media stream.
// Video and format must satisfy Resolve's ownership and descriptor requirements.
// The request uses ctx and the configured HTTP policy. The default 30-second
// total timeout is omitted; an explicit WithHTTPClient timeout is preserved.
// The caller owns the returned stream and must close it; canceling ctx also
// cancels reads. Client.Close does not close a returned stream.
func (c *Client) Open(ctx context.Context, video *Video, format Format) (io.ReadCloser, error) {
	target, err := c.Resolve(ctx, video, format)
	if err != nil {
		return nil, err
	}
	if c.http == nil {
		return nil, ErrHTTPPolicy
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &requestError{"open", err}
	}
	setBrowserHeaders(req)
	hc := *c.http
	if c.defaultHTTP {
		hc.Timeout = 0
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &requestError{"open", err}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &HTTPError{"open", resp.StatusCode}
	}
	return resp.Body, nil
}

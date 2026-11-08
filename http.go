package ytx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxMetadataBytes = 8 << 20
const playerEndpoint = "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"

// ErrHTTPPolicy means borrowed Potter was supplied without WithHTTPClient.
var ErrHTTPPolicy = errors.New("ytx: downstream requests require WithHTTPClient")

// ErrResponseTooLarge indicates a metadata body exceeded 8 MiB.
var ErrResponseTooLarge = errors.New("ytx: metadata response exceeds 8 MiB")

// HTTPError reports a non-200 response without retaining its body.
type HTTPError struct {
	Operation  string
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("ytx: %s HTTP status %d", e.Operation, e.StatusCode)
}

// Avoid including request URLs, response bodies or transport error text in logs.
// The original cause remains available to errors.Is/As, including cancellation.
type requestError struct {
	operation string
	cause     error
}

func (e *requestError) Error() string { return "ytx: " + e.operation + " request failed" }
func (e *requestError) Unwrap() error { return e.cause }

func (c *Client) readMetadata(ctx context.Context, operation, method, target string, body io.Reader, headers ...http.Header) ([]byte, string, error) {
	if c.http == nil {
		return nil, "", ErrHTTPPolicy
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, "", &requestError{operation, err}
	}
	if method == http.MethodPost {
		req.GetBody = nil // Do not make the proof-bearing body replayable.
	}
	setBrowserHeaders(req)
	if len(headers) > 0 {
		for name, values := range headers[0] {
			req.Header[name] = append([]string(nil), values...)
		}
	}
	// Keep the retained transport, timeout, jar and caller redirect restrictions.
	// Add destination validation before a redirect can send proofs elsewhere.
	hc := *c.http
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	var finalURL string
	hc.Transport = metadataTransport(func(dispatched *http.Request) (*http.Response, error) {
		valid := operation == "player script" && dispatched.Method == http.MethodGet && validPlayerScriptURL(dispatched.URL) || operation == "player" && dispatched.URL.String() == playerEndpoint && dispatched.Method == http.MethodPost
		if !valid {
			return nil, errors.New("ytx: metadata destination rejected")
		}
		// Capture before dispatch: neither caller redirect mutation nor missing
		// response metadata may replace the validated final-hop identity.
		target := dispatched.URL.String()
		provenance := dispatched.Clone(dispatched.Context())
		resp, err := transport.RoundTrip(dispatched)
		if err != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			return nil, err
		}
		if resp != nil && resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != target {
			if resp.Body != nil {
				resp.Body.Close()
			}
			return nil, errors.New("ytx: contradictory response provenance")
		}
		if resp != nil {
			resp.Request = provenance
		}
		finalURL = target
		return resp, nil
	})
	hc.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if operation != "player script" || !validPlayerScriptURL(next.URL) || len(via) >= 10 {
			return errors.New("ytx: metadata redirect rejected")
		}
		if c.http.CheckRedirect != nil {
			return c.http.CheckRedirect(next, via)
		}
		return nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", &requestError{operation, err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", &HTTPError{operation, resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return nil, "", &requestError{operation, err}
	}
	if len(raw) > maxMetadataBytes {
		return nil, "", ErrResponseTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return raw, finalURL, nil
}

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

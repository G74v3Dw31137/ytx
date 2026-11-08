package ytx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/G74v3Dw31137/potter"
)

// HomepageConfig contains the complete Innertube request context, including
// request/session fields and unknown fields. Client identity comes from its
// nested client object. UserAgent is normalized to potter.UserAgent, as in Potter.
// Values returned by SessionSnapshot.Config are independent, caller-owned copies.
// Keep this data private.
type HomepageConfig struct {
	Context       json.RawMessage
	ClientName    string
	ClientVersion string
	VisitorData   string
}

// SessionSnapshot is an immutable proof/configuration result. Its generation,
// document URL, tokens and configuration all originate in one atomic Potter
// snapshot. It remains readable after replacement or Close. Its zero value is
// empty. It privately retains the immutable originating homepage (up to 8 MiB per
// acquisition) and parsed configuration, with no client, runtime or lease. Keeping
// snapshots from many acquisitions can retain unbounded total memory.
type SessionSnapshot struct {
	origin      potter.SessionSnapshot
	tokens      potter.Tokens
	generation  uint64
	documentURL string
	config      HomepageConfig
}

// Tokens returns a value copy. Player is bound to the requested video; GVSBinding
// is Potter's selected video ID or visitor identity. Keep proofs private.
func (s SessionSnapshot) Tokens() potter.Tokens { return s.tokens }

// Generation identifies the minter only within the originating Potter client.
// It is not a freshness guarantee or a globally unique session identifier.
func (s SessionSnapshot) Generation() uint64 { return s.generation }

// DocumentURL returns the final homepage URL retained by Potter.
func (s SessionSnapshot) DocumentURL() string { return s.documentURL }

// Config returns an independent copy, including the JSON context's storage.
func (s SessionSnapshot) Config() HomepageConfig {
	cfg := s.config
	cfg.Context = append(json.RawMessage(nil), cfg.Context...)
	return cfg
}

// Format omits session contents from diagnostic output.
func (s SessionSnapshot) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, "ytx.SessionSnapshot{redacted}")
}

// Format omits configuration contents from diagnostic output.
func (c HomepageConfig) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, "ytx.HomepageConfig{redacted}")
}

func snapshotFromPotter(ctx context.Context, origin potter.SessionSnapshot, videoID string) (SessionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SessionSnapshot{}, err
	}
	tokens := origin.Tokens()
	documentURL, reader := origin.Homepage()
	if origin.Generation() == 0 || documentURL == "" || tokens.Player == "" || tokens.GVS == "" {
		return SessionSnapshot{}, configError("incomplete Potter snapshot")
	}
	if tokens.GVSBinding == "" || tokens.GVSBinding != videoID && tokens.GVSBinding != tokens.VisitorData {
		return SessionSnapshot{}, configError("token binding mismatch")
	}
	page, err := io.ReadAll(reader) // Potter bounds retained homepage bodies to 8 MiB.
	if err != nil {
		return SessionSnapshot{}, configError("unreadable homepage")
	}
	cfg, err := parseHomepageConfig(page)
	if err != nil {
		return SessionSnapshot{}, err
	}
	if cfg.VisitorData != tokens.VisitorData || cfg.ClientVersion != tokens.ClientVersion {
		return SessionSnapshot{}, configError("token identity mismatch")
	}
	if err := ctx.Err(); err != nil {
		return SessionSnapshot{}, err
	}
	return SessionSnapshot{origin: origin, tokens: tokens, generation: origin.Generation(), documentURL: documentURL, config: cfg}, nil
}

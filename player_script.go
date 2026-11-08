package ytx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/G74v3Dw31137/ytx/internal/page"
)

// ErrPlayerScript indicates unsupported discovery data or STS syntax.
var ErrPlayerScript = errors.New("ytx: player script unavailable or unsupported")

// playerAsset source/provenance is immutable after construction. URL is the
// homepage-selected identity; finalURL records any validated asset redirect.
// The client-locked preparation slot belongs only to resident registry entries;
// Videos retain source/provenance with that slot cleared.
type playerAsset struct {
	url, finalURL, sha256 string
	source                []byte
	sts                   int
	prepared              *preparation
	acquisition           bool // Client-locked URL authority; cleared on historical admission.
}

func discoverPlayerScript(s SessionSnapshot) (string, error) {
	_, reader := s.origin.Homepage()
	body, err := io.ReadAll(reader) // Immutable Potter input, bounded to 8 MiB.
	if err != nil {
		return "", ErrPlayerScript
	}
	return playerScriptURL(body, s.documentURL)
}

// Recognize the first literal ytcfg object, matching the configuration authority
// used by Potter. These locator positions occur in the historical MWEB capture.
// Do not merge other assignments, inspect a newer session, or evaluate JS.
func playerScriptURL(body []byte, documentURL string) (string, error) {
	var fields map[string]json.RawMessage
	var decodeErr error
	err := page.VisitPageObjects(body, "ytcfg.set", func(object []byte) bool {
		decodeErr = json.Unmarshal(object, &fields)
		return false
	})
	if err != nil || decodeErr != nil || fields == nil {
		return "", ErrPlayerScript
	}
	base, err := url.Parse(documentURL)
	if err != nil || !youtubeHTTPS(base) {
		return "", ErrPlayerScript
	}
	var refs []json.RawMessage
	for _, key := range []string{"jsUrl", "PLAYER_JS_URL"} {
		if raw, ok := fields[key]; ok {
			refs = append(refs, raw)
		}
	}
	if raw, ok := fields["WEB_PLAYER_CONTEXT_CONFIGS"]; ok {
		var configs map[string]json.RawMessage
		if json.Unmarshal(raw, &configs) != nil || configs == nil {
			return "", ErrPlayerScript
		}
		if watch, ok := configs["WEB_PLAYER_CONTEXT_CONFIG_ID_MWEB_WATCH"]; ok {
			var config map[string]json.RawMessage
			if json.Unmarshal(watch, &config) != nil || config == nil {
				return "", ErrPlayerScript
			}
			if ref, ok := config["jsUrl"]; ok {
				refs = append(refs, ref)
			}
		}
	}
	var selected string
	for _, raw := range refs {
		var ref string
		if json.Unmarshal(raw, &ref) != nil || ref == "" || strings.TrimSpace(ref) != ref {
			return "", ErrPlayerScript
		}
		u, err := base.Parse(ref)
		if err != nil || !validPlayerScriptURL(u) {
			return "", ErrPlayerScript
		}
		if selected != "" && selected != u.String() {
			return "", ErrPlayerScript // Conflicting identities fail closed.
		}
		selected = u.String()
	}
	if selected == "" {
		return "", ErrPlayerScript
	}
	return selected, nil
}

func youtubeHTTPS(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Opaque == "" && u.User == nil &&
		u.Fragment == "" && (u.Host == "m.youtube.com" || u.Host == "www.youtube.com")
}

func validPlayerScriptURL(u *url.URL) bool {
	if !youtubeHTTPS(u) || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" ||
		strings.ContainsAny(u.Path, "\\\r\n\t") || !strings.HasPrefix(u.Path, "/s/player/") || !strings.HasSuffix(u.Path, "/base.js") {
		return false
	}
	for _, part := range strings.Split(u.Path, "/")[3:] {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return u.Path != "/s/player/base.js"
}

func (c *Client) downloadPlayer(ctx context.Context, target string) (playerAsset, error) {
	raw, finalURL, err := c.readMetadata(ctx, "player script", "GET", target, nil)
	if err != nil {
		return playerAsset{}, err
	}
	sts, err := extractSTS(raw)
	if err != nil {
		return playerAsset{}, err
	}
	sum := sha256.Sum256(raw)
	return playerAsset{url: target, finalURL: finalURL, sha256: hex.EncodeToString(sum[:]), source: raw, sts: sts}, nil
}

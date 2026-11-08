package ytx

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/G74v3Dw31137/potter"
	"github.com/G74v3Dw31137/ytx/internal/page"
)

// ErrHomepageConfig indicates missing, malformed or inconsistent homepage data.
// Error details contain field names/reasons, never homepage or token contents.
var ErrHomepageConfig = errors.New("ytx: homepage configuration")

func configError(reason string) error { return fmt.Errorf("%w: %s", ErrHomepageConfig, reason) }

func parseHomepageConfig(body []byte) (HomepageConfig, error) {
	var fields map[string]json.RawMessage
	var decodeErr error
	err := page.VisitPageObjects(body, "ytcfg.set", func(object []byte) bool {
		// Match Potter's first-object authority. Later objects never supply missing
		// fields, and we never evaluate JavaScript expressions to produce JSON.
		decodeErr = json.Unmarshal(object, &fields)
		return false
	})
	if err != nil || decodeErr != nil {
		return HomepageConfig{}, configError("malformed ytcfg.set object")
	}
	if fields == nil {
		return HomepageConfig{}, configError("missing ytcfg.set object")
	}
	var inner map[string]json.RawMessage
	if json.Unmarshal(fields["INNERTUBE_CONTEXT"], &inner) != nil || inner == nil {
		return HomepageConfig{}, configError("missing or malformed INNERTUBE_CONTEXT")
	}
	var client map[string]json.RawMessage
	if json.Unmarshal(inner["client"], &client) != nil || client == nil {
		return HomepageConfig{}, configError("missing or malformed INNERTUBE_CONTEXT.client")
	}
	var cfg HomepageConfig
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"clientName", &cfg.ClientName}, {"clientVersion", &cfg.ClientVersion}, {"visitorData", &cfg.VisitorData},
	} {
		if json.Unmarshal(client[field.name], field.value) != nil || *field.value == "" {
			return HomepageConfig{}, configError("missing or malformed client." + field.name)
		}
	}
	if cfg.ClientName != "MWEB" {
		return HomepageConfig{}, configError("unsupported clientName")
	}
	client["userAgent"], _ = json.Marshal(potter.UserAgent)
	inner["client"], _ = json.Marshal(client)
	cfg.Context, _ = json.Marshal(inner)
	return cfg, nil
}

package ytx

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/G74v3Dw31137/potter"
)

const validConfig = `{"INNERTUBE_CONTEXT":{"client":{"clientName":"MWEB","clientVersion":"test-version","visitorData":"test-visitor","userAgent":"page-agent","hl":"en","future":{"x":1}},"request":{"useSsl":true,"marker":"original"},"user":{"lockedSafetyMode":false},"future":[1,2]}}`

func configPage(object string) []byte { return []byte("<script>ytcfg.set(" + object + ");</script>") }

func TestParseHomepageConfig(t *testing.T) {
	cfg, err := parseHomepageConfig(configPage(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientName != "MWEB" || cfg.ClientVersion != "test-version" || cfg.VisitorData != "test-visitor" {
		t.Fatalf("wrong identity: %q/%q/%q", cfg.ClientName, cfg.ClientVersion, cfg.VisitorData)
	}
	var expected, got map[string]any
	var original map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validConfig), &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(original["INNERTUBE_CONTEXT"], &expected); err != nil {
		t.Fatal(err)
	}
	expected["client"].(map[string]any)["userAgent"] = potter.UserAgent
	if err := json.Unmarshal(cfg.Context, &got); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(expected)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatal("full context was not preserved/normalized")
	}
	// The harness needs neither a homepage API key nor top-level client fields;
	// STS comes from player JavaScript, which P1 does not fetch.
	again, err := parseHomepageConfig(configPage(validConfig))
	if err != nil || string(again.Context) != string(cfg.Context) {
		t.Fatal("nondeterministic extraction")
	}
}

func TestHomepageConfigErrors(t *testing.T) {
	for name, body := range map[string][]byte{
		"empty":              nil,
		"no configuration":   []byte("<html><script>void 0;</script></html>"),
		"bare JavaScript":    []byte("ytcfg.set(" + validConfig + ");"),
		"malformed JSON":     configPage(`{"PRIVATE": undefined}`),
		"unbalanced":         configPage(`{"PRIVATE": [}`),
		"unclosed call":      []byte(`<script>ytcfg.set({"PRIVATE":1}</script>`),
		"missing context":    configPage(`{}`),
		"null context":       configPage(`{"INNERTUBE_CONTEXT":null}`),
		"array context":      configPage(`{"INNERTUBE_CONTEXT":[]}`),
		"missing client":     configPage(`{"INNERTUBE_CONTEXT":{}}`),
		"null client":        configPage(`{"INNERTUBE_CONTEXT":{"client":null}}`),
		"string client":      configPage(`{"INNERTUBE_CONTEXT":{"client":"PRIVATE"}}`),
		"unsupported client": configPage(strings.Replace(validConfig, `"MWEB"`, `"WEB"`, 1)),
		"missing name":       configPage(strings.Replace(validConfig, `"clientName":"MWEB",`, "", 1)),
		"missing version":    configPage(strings.Replace(validConfig, `"clientVersion":"test-version",`, "", 1)),
		"missing visitor":    configPage(strings.Replace(validConfig, `"visitorData":"test-visitor",`, "", 1)),
		"empty version":      configPage(strings.Replace(validConfig, `"test-version"`, `""`, 1)),
		"null version":       configPage(strings.Replace(validConfig, `"test-version"`, `null`, 1)),
		"numeric version":    configPage(strings.Replace(validConfig, `"test-version"`, `123`, 1)),
		"null visitor":       configPage(strings.Replace(validConfig, `"test-visitor"`, `null`, 1)),
		"unclosed script":    []byte("<script>ytcfg.set(" + validConfig + ");"),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseHomepageConfig(body)
			if !errors.Is(err, ErrHomepageConfig) {
				t.Fatalf("expected configuration error, got %v", err)
			}
			if cfg.Context != nil || cfg.ClientName != "" {
				t.Fatal("failure returned partial configuration")
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("error exposed input")
			}
		})
	}
}

func TestHomepageEscapesAndJavaScript(t *testing.T) {
	object := strings.Replace(validConfig, `"marker":"original"`, `"marker":"quote: \" slash: \\ delimiters: }); unicode: \u263a &amp;"`, 1)
	for name, surround := range map[string]string{
		"comments":       "// ytcfg.set({\"fake\":true});\n/* ytcfg.set({}); */\n",
		"strings":        `const x='ytcfg.set({})'; const y="ytcfg.set({})";`,
		"regex":          `const re=/[ytcfg.set({})]/;`,
		"template":       "const x=`ytcfg.set({})`;",
		"unrelated code": `window.foo={nested:[{a:"}"}]}; (function(){})();`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte("<p>Don't execute this</p><script>" + surround + "ytcfg.set /* comment */ (\n" + object + "\n); window.after();</script>")
			cfg, err := parseHomepageConfig(body)
			if err != nil {
				t.Fatal(err)
			}
			var context struct{ Request struct{ Marker string } }
			if err := json.Unmarshal(cfg.Context, &context); err != nil {
				t.Fatal(err)
			}
			if context.Request.Marker != "quote: \" slash: \\ delimiters: }); unicode: ☺ &amp;" {
				t.Fatalf("bad escaping: %q", context.Request.Marker)
			}
		})
	}
}

func TestHomepageSelection(t *testing.T) {
	for _, prefix := range []string{
		`<!-- <script>ytcfg.set({});</script> -->`,
		`<script type=application/json>ytcfg.set({});</script>`,
		`<script src="other.js">ytcfg.set({});</script>`,
		`<template><script>ytcfg.set({});</script></template>`,
		`<script type=module>ytcfg.set({});</script>`,
		`<p data-x='ytcfg.set({});'>ytcfg.set({});</p>`,
		`<script>myytcfg.set({}); obj.ytcfg.set({}); ytcfg.setter({});</script>`,
	} {
		if _, err := parseHomepageConfig(append([]byte(prefix), configPage(validConfig)...)); err != nil {
			t.Fatal(err)
		}
	}
	// A later call must neither replace nor complete the authoritative object.
	first := configPage(validConfig)
	later := configPage(strings.ReplaceAll(validConfig, "test-visitor", "later-visitor"))
	cfg, err := parseHomepageConfig(append(first, later...))
	if err != nil || cfg.VisitorData != "test-visitor" {
		t.Fatal("first configuration lost authority")
	}
	for _, first := range []string{`{}`, `{"bad":undefined}`} {
		if _, err := parseHomepageConfig(append(configPage(first), configPage(validConfig)...)); !errors.Is(err, ErrHomepageConfig) {
			t.Fatal("later configuration masked invalid first configuration")
		}
	}
}

func TestHomepageBounds(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(strings.Repeat("x", (8<<20)+1)),
		configPage(`{"deep":` + strings.Repeat("[", 256) + "0" + strings.Repeat("]", 256) + "}"),
	} {
		if _, err := parseHomepageConfig(body); !errors.Is(err, ErrHomepageConfig) {
			t.Fatalf("expected bounded parsing error, got %v", err)
		}
	}
}

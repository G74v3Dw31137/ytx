package ytx

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/G74v3Dw31137/ytx/internal/player"
)

const maxMediaURL = 128 << 10
const maxQueryFields = 512

var signatureName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type mediaInput struct{ raw, s, sp string }

func parseMedia(f Format) (mediaInput, error) {
	cipher := f.SignatureCipher
	if cipher == "" {
		cipher = f.Cipher
	}
	if cipher == "" {
		if f.URL == "" {
			return mediaInput{}, ErrUnsupportedFormat
		}
		return mediaInput{raw: f.URL}, nil
	}
	if len(cipher) > maxMediaURL*3 {
		return mediaInput{}, ErrInvalidCipher
	}
	fields, e := url.ParseQuery(cipher)
	if e != nil {
		return mediaInput{}, ErrInvalidCipher
	}
	for k, v := range fields {
		if (k != "url" && k != "s" && k != "sp") || len(v) != 1 || v[0] == "" {
			return mediaInput{}, ErrInvalidCipher
		}
	}
	raw, s := fields.Get("url"), fields.Get("s")
	sp := "signature"
	if v, ok := fields["sp"]; ok {
		sp = v[0]
	}
	if raw == "" || s == "" || len(s) > player.MaxValue || !validSignatureName(sp) {
		return mediaInput{}, ErrInvalidCipher
	}
	return mediaInput{raw: raw, s: s, sp: sp}, nil
}

func validSignatureName(k string) bool {
	if !signatureName.MatchString(k) {
		return false
	}
	switch strings.ToLower(k) {
	case "url", "s", "sp", "n", "v", "pot", "sparams", "lsparams", "lsig", "expire", "sig", "signature":
		return k == "sig" || k == "signature"
	}
	// Conservatively permit custom signature names only after checking signing lists.
	return true
}

type rawField struct{ raw, key string }
type mediaQuery struct {
	u         *url.URL
	fields    []rawField
	protected map[string]bool
}

func validEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) {
				return false
			}
			for _, c := range []byte{s[i+1], s[i+2]} {
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
					return false
				}
			}
			i += 2
		}
	}
	return true
}
func newMediaQuery(raw string) (*mediaQuery, error) {
	if len(raw) == 0 || len(raw) > maxMediaURL || strings.ContainsAny(raw, "\r\n\t\\#") {
		return nil, ErrInvalidURL
	}
	for _, c := range []byte(raw) {
		if c <= 0x20 || c >= 0x7f {
			return nil, ErrInvalidURL
		}
	}
	u, e := url.Parse(raw)
	if e != nil {
		return nil, ErrInvalidURL
	}
	host := u.Hostname()
	if len(host) > 253 {
		return nil, ErrInvalidURL
	}
	if u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Host != host || !strings.HasSuffix(host, ".googlevideo.com") || u.Path != "/videoplayback" {
		return nil, ErrInvalidURL
	}
	prefix := strings.TrimSuffix(host, ".googlevideo.com")
	if prefix == "" {
		return nil, ErrInvalidURL
	}
	for _, label := range strings.Split(prefix, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, ErrInvalidURL
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return nil, ErrInvalidURL
			}
		}
	}
	q := &mediaQuery{u: u, protected: map[string]bool{"sparams": true, "lsparams": true, "lsig": true}}
	if u.RawQuery != "" {
		segments := strings.Split(u.RawQuery, "&")
		if len(segments) > maxQueryFields {
			return nil, ErrInvalidURL
		}
		for _, part := range segments {
			k, _, _ := strings.Cut(part, "=")
			if !validEscapes(part) {
				return nil, ErrInvalidURL
			}
			key, e := url.QueryUnescape(k)
			if e != nil {
				return nil, ErrInvalidURL
			}
			q.fields = append(q.fields, rawField{raw: part, key: key})
		}
	}
	return q, nil
}
func (q *mediaQuery) value(key string) (value string, exists bool, err error) {
	for _, f := range q.fields {
		if f.key == key {
			if exists {
				return "", false, ErrInvalidURL
			}
			exists = true
			_, raw, has := strings.Cut(f.raw, "=")
			if !has {
				return "", false, ErrInvalidURL
			}
			value, err = url.QueryUnescape(raw)
			if err != nil {
				return "", false, ErrInvalidURL
			}
		}
	}
	return
}
func (q *mediaQuery) validate(sp string, cipher bool) error {
	for _, key := range []string{"n", "pot", "s", "sp", "sig", "signature", "expire", "sparams", "lsparams", "lsig"} {
		value, exists, e := q.value(key)
		if e != nil {
			return e
		}
		if exists && (key == "sig" || key == "signature" || key == "lsig") && value == "" {
			return ErrInvalidURL
		}
		if exists && (key == "s" || key == "sp") {
			return ErrInvalidURL
		}
		if exists && (key == "sparams" || key == "lsparams") {
			if value == "" {
				return ErrInvalidURL
			}
			for _, name := range strings.Split(value, ",") {
				if !signatureName.MatchString(name) {
					return ErrInvalidURL
				}
				q.protected[name] = true
			}
		}
		if key == "expire" && exists {
			seconds, e := strconv.ParseInt(value, 10, 64)
			if e != nil || seconds <= 0 {
				return ErrInvalidURL
			}
			if seconds <= time.Now().Unix() {
				return ErrExpiredURL
			}
		}
	}
	_, hasSig, _ := q.value("sig")
	_, hasSignature, _ := q.value("signature")
	if hasSig && hasSignature {
		return ErrInvalidURL
	}
	if cipher {
		if !validSignatureName(sp) || q.protected[sp] {
			return ErrInvalidCipher
		}
		for _, key := range []string{sp, "sig", "signature"} {
			if _, exists, e := q.value(key); e != nil || exists {
				return ErrInvalidCipher
			}
		}
	}
	return nil
}

// edit changes only a targeted value; every other raw field remains byte-exact.
func (q *mediaQuery) edit(key, value string) error {
	old, exists, e := q.value(key)
	if e != nil {
		return e
	}
	if exists && old == value {
		return nil
	}
	if q.protected[key] {
		return ErrInvalidURL
	}
	encoded := url.QueryEscape(value)
	for i, f := range q.fields {
		if f.key == key {
			rawKey, _, _ := strings.Cut(f.raw, "=")
			q.fields[i].raw = rawKey + "=" + encoded
			return nil
		}
	}
	if len(q.fields) >= maxQueryFields {
		return ErrInvalidURL
	}
	q.fields = append(q.fields, rawField{raw: url.QueryEscape(key) + "=" + encoded, key: key})
	return nil
}
func (q *mediaQuery) String() string {
	parts := make([]string, len(q.fields))
	for i, f := range q.fields {
		parts[i] = f.raw
	}
	q.u.RawQuery = strings.Join(parts, "&")
	return q.u.String()
}

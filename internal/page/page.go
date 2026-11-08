// Copyright (c) 2026 G74v3Dw31137
// SPDX-License-Identifier: MIT
// Adapted from Potter v0.2.0; see THIRD_PARTY.md.

package page

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
)

const maxPageBytes = 8 << 20
const maxPageNesting = 256

func invalidPageData() error { return errors.New("invalid homepage data") }

// VisitPageObjects visits object payloads in eligible inline classic scripts, in
// document order. Returning false stops discovery, including suffix errors.
// It recognizes data calls, not execution, bindings, or general JavaScript syntax.
func VisitPageObjects(page []byte, call string, visit func([]byte) bool) error {
	if len(page) > maxPageBytes {
		return invalidPageData()
	}
	z := html.NewTokenizer(bytes.NewReader(page))
	z.SetMaxBuf(maxPageBytes + 1)
	var excluded []string // Conservative containment; no DOM or HTML tree repair.
	offset, bodyStart := 0, -1
	eligible := false
	for {
		tt := z.Next()
		start := offset
		offset += len(z.Raw()) // Before Token, which can normalize the tokenizer buffer.
		switch tt {
		case html.ErrorToken:
			if z.Err() != io.EOF {
				return invalidPageData()
			}
			// An unterminated script is never visited: only complete script
			// elements contribute candidates. Do not scan the truncated suffix.
			return nil
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "template", "svg", "math":
				// A slash does not self-close HTML template elements. Foreign containers
				// are deliberately excluded even when a browser might execute descendants.
				if tt == html.SelfClosingTagToken && t.Data != "template" {
					continue
				}
				if len(excluded) == maxPageNesting {
					return invalidPageData()
				}
				excluded = append(excluded, t.Data)
			case "script":
				bodyStart = offset
				eligible = len(excluded) == 0 && eligiblePageScript(t.Attr)
			}
		case html.EndTagToken:
			t := z.Token()
			if t.Data == "script" && bodyStart >= 0 {
				if eligible {
					stopped, err := scanPageScript(page[bodyStart:start], call, visit)
					if err != nil || stopped {
						return err
					}
				}
				bodyStart, eligible = -1, false
			}
			if len(excluded) > 0 && t.Data == excluded[len(excluded)-1] {
				excluded = excluded[:len(excluded)-1]
			}
		}
	}
}

func eligiblePageScript(attrs []html.Attribute) bool {
	// HTML uses the first occurrence of a duplicate attribute.
	values := make(map[string]string)
	for _, a := range attrs {
		if _, ok := values[a.Key]; !ok {
			values[a.Key] = a.Val
		}
	}
	if _, ok := values["src"]; ok {
		return false
	}
	if _, ok := values["nomodule"]; ok {
		return false
	}
	typ, present := values["type"]
	if !present {
		if lang := values["language"]; lang != "" {
			typ = "text/" + lang
		}
	}
	typ = strings.ToLower(strings.Trim(typ, " \t\n\r\f"))
	switch typ {
	case "", "application/ecmascript", "application/javascript", "application/x-ecmascript", "application/x-javascript",
		"text/ecmascript", "text/javascript", "text/javascript1.0", "text/javascript1.1", "text/javascript1.2",
		"text/javascript1.3", "text/javascript1.4", "text/javascript1.5", "text/jscript", "text/livescript",
		"text/x-ecmascript", "text/x-javascript":
		return true
	}
	return false // Includes modules, import maps, data blocks and MIME parameters.
}

// scanPageScript is also the low-level entry point for bare-JavaScript tests.
// The lexer and payload stack advance monotonically and never recurse.
func scanPageScript(source []byte, call string, visit func([]byte) bool) (bool, error) {
	l := pageLexer{source: source, expression: true, lineStart: true}
	state := 0
	for {
		t := l.next()
		if l.tooDeep {
			return false, invalidPageData()
		}
		if t.kind == 0 {
			return false, nil
		} // Malformed unrelated JS stays in this script.
		switch state {
		case 1:
			if t.kind == '(' {
				state = 2
				continue
			}
		case 2:
			if t.kind == '{' {
				start := t.start
				stack := []byte{'{'}
				for len(stack) > 0 {
					t = l.next()
					if t.kind == 0 || l.tooDeep {
						return false, invalidPageData()
					}
					switch t.kind {
					case '{', '[', '(':
						if len(stack) == maxPageNesting {
							return false, invalidPageData()
						}
						stack = append(stack, t.kind)
					case '}', ']', ')':
						open := stack[len(stack)-1]
						if open == '{' && t.kind != '}' || open == '[' && t.kind != ']' || open == '(' && t.kind != ')' {
							return false, invalidPageData()
						}
						stack = stack[:len(stack)-1]
					}
				}
				end := t.end
				if l.next().kind != ')' || l.tooDeep {
					return false, invalidPageData()
				}
				if !visit(source[start:end]) {
					return true, nil
				}
				state = 0
				continue
			}
		}
		state = 0
		if t.kind == 'w' && string(source[t.start:t.end]) == call {
			state = 1
		}
	}
}

// Copyright (c) 2026 G74v3Dw31137
// SPDX-License-Identifier: MIT
// Adapted from Potter v0.2.0; see THIRD_PARTY.md.

package page

import (
	"bytes"
	"unicode"
	"unicode/utf8"
)

type pageToken struct {
	kind       byte
	start, end int
}
type pageTemplate struct {
	text   bool
	braces int
}

// Slash recognition uses expression context, including control parentheses.
// It is deliberately not a JS grammar: regex after a statement block's '}' and
// some declaration/ASI contexts remain ambiguous.
type pageLexer struct {
	source                         []byte
	pos                            int
	expression, lineStart, tooDeep bool
	previous                       string
	parens                         []bool
	templates                      []pageTemplate
}

func pageLine(c rune) bool { return c == '\n' || c == '\r' || c == '\u2028' || c == '\u2029' }
func pageSpace(c rune) bool {
	return pageLine(c) || c == '\t' || c == '\v' || c == '\f' || c == '\uFEFF' || unicode.Is(unicode.Zs, c)
}
func pageWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c == '.' || c >= utf8.RuneSelf || c == '\\'
}

func (l *pageLexer) trivia() {
	s := l.source
	for l.pos < len(s) {
		r, n := utf8.DecodeRune(s[l.pos:])
		if pageSpace(r) {
			if pageLine(r) {
				l.lineStart = true
			}
			l.pos += n
			continue
		}
		tail := s[l.pos:]
		if bytes.HasPrefix(tail, []byte("//")) || bytes.HasPrefix(tail, []byte("<!--")) || l.lineStart && bytes.HasPrefix(tail, []byte("-->")) {
			for l.pos < len(s) {
				r, n = utf8.DecodeRune(s[l.pos:])
				if pageLine(r) {
					break
				}
				l.pos += n
			}
			continue
		}
		if bytes.HasPrefix(tail, []byte("/*")) {
			l.pos += 2
			for l.pos < len(s) && !bytes.HasPrefix(s[l.pos:], []byte("*/")) {
				r, n = utf8.DecodeRune(s[l.pos:])
				if pageLine(r) {
					l.lineStart = true
				}
				l.pos += n
			}
			if l.pos < len(s) {
				l.pos += 2
			}
			continue
		}
		break
	}
}

func (l *pageLexer) next() pageToken {
	s := l.source
	if len(l.templates) > 0 && l.templates[len(l.templates)-1].text {
		return l.templateText()
	}
	l.trivia()
	start := l.pos
	if start >= len(s) || l.tooDeep {
		return pageToken{}
	}
	l.lineStart = false
	c := s[l.pos]
	l.pos++
	kind := c
	switch {
	case c == '\'' || c == '"':
		kind = 'v'
		closed := false
		for l.pos < len(s) {
			b := s[l.pos]
			l.pos++
			if b == '\\' {
				if l.pos < len(s) {
					_, n := utf8.DecodeRune(s[l.pos:])
					l.pos += n
				}
				continue
			}
			if b == c {
				closed = true
				break
			}
		}
		if !closed {
			return pageToken{}
		}
		l.expression = false
	case c == '`':
		if len(l.templates) == maxPageNesting {
			l.tooDeep = true
			return pageToken{}
		}
		l.templates = append(l.templates, pageTemplate{text: true})
		return l.templateText()
	case c == '/' && l.expression:
		kind = 'v'
		class, closed := false, false
		for l.pos < len(s) {
			r, n := utf8.DecodeRune(s[l.pos:])
			if pageLine(r) {
				break
			}
			l.pos += n
			if r == '\\' {
				if l.pos < len(s) {
					r, n = utf8.DecodeRune(s[l.pos:])
					if pageLine(r) {
						break
					}
					l.pos += n
				}
				continue
			}
			if r == '[' {
				class = true
			}
			if r == ']' {
				class = false
			}
			if r == '/' && !class {
				closed = true
				break
			}
		}
		if !closed {
			l.pos = len(s)
			return pageToken{}
		}
		l.wordEnd()
		l.expression = false
	case pageWord(c) && c != '.':
		kind = 'w'
		l.wordEnd()
		word := string(s[start:l.pos])
		switch word {
		case "return", "throw", "case", "delete", "void", "typeof", "new", "in", "instanceof", "yield", "await", "else", "do":
			l.expression = true
		default:
			l.expression = false
		}
	case c == '(':
		if len(l.parens) == maxPageNesting {
			l.tooDeep = true
			return pageToken{}
		}
		control := false
		switch l.previous {
		case "if", "while", "for", "with", "switch", "catch":
			control = true
		}
		l.parens = append(l.parens, control)
		l.expression = true
	case c == ')':
		l.expression = false
		if len(l.parens) > 0 {
			l.expression = l.parens[len(l.parens)-1]
			l.parens = l.parens[:len(l.parens)-1]
		}
	case c == '{':
		if len(l.templates) > 0 {
			l.templates[len(l.templates)-1].braces++
		}
		l.expression = true
	case c == '}':
		if len(l.templates) > 0 {
			frame := &l.templates[len(l.templates)-1]
			if frame.braces == 0 {
				frame.text = true
				return l.templateText()
			}
			frame.braces--
		}
		l.expression = false
	case c == ']':
		l.expression = false
	case c == '.':
		l.expression = false
	case c == '+' || c == '-':
		if l.pos < len(s) && s[l.pos] == c {
			l.pos++
		} else {
			l.expression = true
		}
	default:
		l.expression = true
	}
	l.previous = string(s[start:l.pos])
	return pageToken{kind: kind, start: start, end: l.pos}
}

// Non-ASCII identifier bytes must not absorb adjacent Unicode whitespace.
func (l *pageLexer) wordEnd() {
	for l.pos < len(l.source) && pageWord(l.source[l.pos]) {
		r, n := utf8.DecodeRune(l.source[l.pos:])
		if pageSpace(r) {
			break
		}
		l.pos += n
	}
}

// Template text is opaque; interpolation is executable source. Boundary tokens
// are operands, not delimiters, so payload balancing sees only actual JS braces.
func (l *pageLexer) templateText() pageToken {
	start := l.pos
	s := l.source
	for l.pos < len(s) {
		c := s[l.pos]
		l.pos++
		if c == '\\' {
			if l.pos < len(s) {
				l.pos++
			}
			continue
		}
		if c == '`' {
			l.templates = l.templates[:len(l.templates)-1]
			l.expression = false
			l.previous = "`"
			l.lineStart = false
			return pageToken{kind: 'v', start: start, end: l.pos}
		}
		if c == '$' && l.pos < len(s) && s[l.pos] == '{' {
			l.pos++
			l.templates[len(l.templates)-1].text = false
			l.expression = true
			l.previous = "${"
			l.lineStart = false
			return pageToken{kind: 'v', start: start, end: l.pos}
		}
	}
	return pageToken{}
}

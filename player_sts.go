package ytx

import (
	"bytes"
	"fmt"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const maxSTSNesting = 256

// extractSTS recognizes literal data properties, not assignments or execution.
// Scan the entire bounded input: equal literals agree; distinct literals, an
// unsupported named value, or ambiguous lexing fail rather than picking first.
// Full assets require balanced delimiters, including the suffix after a candidate.
func extractSTS(source []byte) (int, error) {
	if !utf8.Valid(source) {
		return 0, stsError("invalid UTF-8")
	}
	if len(source) > maxMetadataBytes {
		return 0, stsError("source exceeds limit")
	}
	l := stsLexer{source: source, expression: true, lineStart: true}
	type frame struct {
		kind   string
		object byte // 'o': expression object, 'b': block, '?': unsupported context
	}
	var stack []frame
	const (
		wantKey = iota
		wantColon
		wantValue
		wantDelimiter
	)
	previous, state, selected := "", wantKey, 0
	boundary := false
	ambiguous := false
	for {
		t, err := l.next(0)
		if err != nil {
			return 0, err
		}
		if t == "" {
			break
		}
		switch state {
		case wantColon:
			if t == ":" {
				if ambiguous {
					return 0, stsError("ambiguous property context")
				}
				state = wantValue
				previous = t
				continue
			}
			state = wantKey // A method or shorthand is not a literal data property.
		case wantValue:
			// Decimal only, with no leading zero, sign, separators or suffix.
			if t[0] < '1' || t[0] > '9' || bytes.IndexFunc([]byte(t), func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return 0, stsError("unsupported numeric literal")
			}
			n, err := strconv.ParseInt(t, 10, 32)
			if err != nil {
				return 0, stsError("overflowing literal")
			}
			if selected != 0 && selected != int(n) {
				return 0, stsError("conflicting literals")
			}
			selected, state, previous = int(n), wantDelimiter, t
			continue
		case wantDelimiter:
			if t != "," && t != "}" {
				return 0, stsError("computed or malformed value")
			}
			state = wantKey
		}
		if boundary && stsKey(t) {
			state = wantColon
			ambiguous = len(stack) > 0 && stack[len(stack)-1].object == '?'
		}
		boundary = false
		switch t {
		case "{", "[", "(":
			if len(stack) == maxSTSNesting {
				return 0, stsError("nesting exceeds limit")
			}
			// These unambiguous expression positions cover the observed object
			// literal. Braces in statement/label/arrow-body positions are blocks.
			object := byte('b')
			if t == "{" {
				switch previous {
				case "=", "(", "[", ",", "?", "!", "~":
					object = 'o'
				case ":":
					if len(stack) > 0 && stack[len(stack)-1].object == 'o' {
						object = 'o'
					} else {
						object = '?'
					}
				case "return":
					if !l.newline {
						object = 'o'
					}
				case "", ";", "{", "}", ")", "=>", "else", "do", "try", "finally":
					// Statement blocks, including labels, are not data objects.
				default:
					object = '?'
				}
			}
			stack = append(stack, frame{t, object})
			boundary = t == "{" && object != 'b'
		case "}", "]", ")":
			if len(stack) == 0 {
				return 0, stsError("unmatched closing delimiter")
			}
			if len(stack) > 0 {
				open := stack[len(stack)-1].kind
				if open == "{" && t != "}" || open == "[" && t != "]" || open == "(" && t != ")" {
					return 0, stsError("unbalanced delimiters")
				}
				stack = stack[:len(stack)-1]
			}
		case ",":
			boundary = len(stack) > 0 && stack[len(stack)-1].kind == "{" && stack[len(stack)-1].object != 'b'
		}
		previous = t
	}
	if selected == 0 || state != wantKey || len(stack) != 0 {
		return 0, stsError("missing or incomplete literal")
	}
	return selected, nil
}

func stsKey(t string) bool {
	return t == "signatureTimestamp" || t == "sts" || t == `"signatureTimestamp"` || t == `"sts"` || t == "'signatureTimestamp'" || t == "'sts'"
}

func stsError(reason string) error { return fmt.Errorf("%w: STS %s", ErrPlayerScript, reason) }

// The homepage lexer is intentionally permissive on malformed suffixes and
// exposes template interpolations. STS needs opaque templates and explicit
// errors, so this small lexer is separate. It never evaluates JavaScript.
type stsLexer struct {
	source                         []byte
	pos                            int
	expression, lineStart, newline bool
	previous, beforePrevious       string
	parens                         []bool
}

func stsLine(r rune) bool { return r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' }
func stsSpace(r rune) bool {
	return stsLine(r) || r == '\t' || r == '\v' || r == '\f' || r == '\uFEFF' || unicode.Is(unicode.Zs, r)
}
func stsIdentifierRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '\u200c' || r == '\u200d' || unicode.Is(unicode.Pc, r)
}

func stsWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c == '\\' || c >= utf8.RuneSelf
}

func (l *stsLexer) trivia() error {
	s := l.source
	l.newline = false
	for l.pos < len(s) {
		r, n := utf8.DecodeRune(s[l.pos:])
		if stsSpace(r) {
			l.pos += n
			if stsLine(r) {
				l.newline, l.lineStart = true, true
			}
			continue
		}
		tail := s[l.pos:]
		if bytes.HasPrefix(tail, []byte("//")) || l.pos == 0 && bytes.HasPrefix(tail, []byte("#!")) || bytes.HasPrefix(tail, []byte("<!--")) || l.lineStart && bytes.HasPrefix(tail, []byte("-->")) {
			for l.pos < len(s) {
				r, n = utf8.DecodeRune(s[l.pos:])
				if stsLine(r) {
					break
				}
				l.pos += n
			}
			continue
		}
		if bytes.HasPrefix(tail, []byte("/*")) {
			end := bytes.Index(tail[2:], []byte("*/"))
			if end < 0 {
				return stsError("unterminated comment")
			}
			for _, r := range string(tail[:end+4]) {
				if stsLine(r) {
					l.newline, l.lineStart = true, true
				}
			}
			l.pos += end + 4
			continue
		}
		break
	}
	return nil
}

func (l *stsLexer) next(depth int) (string, error) {
	if err := l.trivia(); err != nil {
		return "", err
	}
	s, start := l.source, l.pos
	if start == len(s) {
		return "", nil
	}
	l.lineStart = false
	c := s[l.pos]
	_, width := utf8.DecodeRune(s[l.pos:])
	l.pos += width
	opaque := false
	switch {
	case c == '#':
		// PrivateIdentifier is one token, never punctuation plus a keyword.
		// In particular #if(...) is a call, and #return ends an expression.
		// Escaped names are outside this subset; reject before splitting them.
		if l.pos == len(s) {
			return "", stsError("incomplete private identifier")
		}
		r, n := utf8.DecodeRune(s[l.pos:])
		if r != '_' && r != '$' && !unicode.IsLetter(r) {
			return "", stsError("unsupported private identifier")
		}
		l.pos += n
		for l.pos < len(s) {
			r, n = utf8.DecodeRune(s[l.pos:])
			if r == '\\' {
				return "", stsError("escaped private identifier")
			}
			if r != '$' && !stsIdentifierRune(r) {
				break
			}
			l.pos += n
		}
		l.expression = false
	case c == '\'' || c == '"':
		if err := l.quoted(c); err != nil {
			return "", err
		}
		l.expression = false
		opaque = l.pos-start > len(`"signatureTimestamp"`)
	case c == '`':
		if err := l.template(depth + 1); err != nil {
			return "", err
		}
		l.expression = false
		opaque = true
	case c == '/':
		// A closing brace can end a block or an object/function expression.
		// Restricted statements and postfix operators also have ASI ambiguity.
		if l.previous == "}" || l.previous == "++" || l.previous == "--" || l.previous == "break" || l.previous == "continue" || l.previous == "of" || l.previous == "await" || l.previous == "yield" || l.newline && !l.expression {
			return "", stsError("ambiguous slash")
		}
		if l.expression {
			if err := l.regexp(); err != nil {
				return "", err
			}
			l.expression = false
			opaque = true
		} else {
			l.expression = true // Division cannot contain property tokens.
		}
	case stsWord(c) || c == '.':
		if width > 1 {
			r, _ := utf8.DecodeRune(s[start:])
			if !stsIdentifierRune(r) {
				return "", stsError("unsupported identifier")
			}
		}
		for l.pos < len(s) && (stsWord(s[l.pos]) || s[l.pos] == '.') {
			r, n := utf8.DecodeRune(s[l.pos:])
			if stsSpace(r) {
				break
			}
			if n > 1 && !stsIdentifierRune(r) {
				return "", stsError("unsupported identifier")
			}
			l.pos += n
		}
		switch string(s[start:l.pos]) {
		case "return", "throw", "case", "delete", "void", "typeof", "new", "in", "instanceof", "yield", "await", "else", "do", "default", "extends":
			l.expression = true
		default:
			l.expression = false
		}
		if stsMemberDot(l.previous) {
			l.expression = false // Reserved words are legal member names.
		}
	case c == '(':
		if len(l.parens) == maxSTSNesting {
			return "", stsError("parentheses exceed limit")
		}
		control := l.previous == "if" || l.previous == "while" || l.previous == "for" || l.previous == "with" || l.previous == "switch" || l.previous == "catch" || l.previous == "await" && l.beforePrevious == "for"
		control = control && !stsMemberDot(l.beforePrevious)
		l.parens = append(l.parens, control)
		l.expression = true
	case c == ')':
		l.expression = false
		if len(l.parens) > 0 {
			l.expression = l.parens[len(l.parens)-1]
			l.parens = l.parens[:len(l.parens)-1]
		}
	case c == '}' || c == ']':
		l.expression = false
	case c == '+' || c == '-':
		if l.pos < len(s) && s[l.pos] == c {
			l.pos++
		} else {
			l.expression = true
		}
	case c == '=' && l.pos < len(s) && s[l.pos] == '>':
		l.pos++
		l.expression = true
	default:
		l.expression = true
	}
	t := "<literal>"
	if !opaque {
		t = string(s[start:l.pos])
	}
	l.beforePrevious, l.previous = l.previous, t
	return t, nil
}

func stsMemberDot(t string) bool { return len(t) > 0 && t[len(t)-1] == '.' }

func (l *stsLexer) quoted(quote byte) error {
	for l.pos < len(l.source) {
		c := l.source[l.pos]
		l.pos++
		if c == quote {
			return nil
		}
		if c == '\\' && l.pos < len(l.source) {
			// Consume one escaped rune; CRLF is one line continuation.
			r, n := utf8.DecodeRune(l.source[l.pos:])
			l.pos += n
			if r == '\r' && l.pos < len(l.source) && l.source[l.pos] == '\n' {
				l.pos++
			}
		} else if c == '\n' || c == '\r' {
			return stsError("unescaped string newline")
		}
	}
	return stsError("unterminated string")
}

func (l *stsLexer) regexp() error {
	class := false
	for l.pos < len(l.source) {
		r, n := utf8.DecodeRune(l.source[l.pos:])
		l.pos += n
		if stsLine(r) {
			break
		}
		if r == '\\' && l.pos < len(l.source) {
			r, n = utf8.DecodeRune(l.source[l.pos:])
			if stsLine(r) {
				break
			}
			l.pos += n
			continue
		}
		if r == '[' {
			if class {
				return stsError("unsupported nested regexp class")
			}
			class = true
		} else if r == ']' {
			class = false
		} else if r == '/' && !class {
			seen := ""
			for l.pos < len(l.source) {
				r, n := utf8.DecodeRune(l.source[l.pos:])
				if stsSpace(r) {
					break
				}
				if !stsWord(l.source[l.pos]) {
					break
				}
				// v uses nested Unicode-set classes, outside this scanner's subset.
				if n != 1 || !bytes.ContainsRune([]byte("dgimsuy"), r) || bytes.ContainsRune([]byte(seen), r) {
					return stsError("unsupported regexp flags")
				}
				seen += string(r)
				l.pos += n
			}
			return nil
		}
	}
	return stsError("unterminated regexp")
}

// Templates, including interpolations, are opaque to candidate discovery.
// Interpolations are lexed only to find their end, including nested templates,
// strings, comments and regexps. Recursion and brace depth are both bounded.
func (l *stsLexer) template(depth int) error {
	if depth > maxSTSNesting {
		return stsError("template nesting exceeds limit")
	}
	for l.pos < len(l.source) {
		c := l.source[l.pos]
		l.pos++
		if c == '\\' && l.pos < len(l.source) {
			l.pos++
		} else if c == '`' {
			return nil
		} else if c == '$' && l.pos < len(l.source) && l.source[l.pos] == '{' {
			l.pos++
			l.expression, l.previous = true, "${"
			delimiters := []string{"{"}
			for len(delimiters) > 0 {
				t, err := l.next(depth)
				if err != nil {
					return err
				}
				if t == "" {
					return stsError("unterminated interpolation")
				}
				switch t {
				case "{", "[", "(":
					delimiters = append(delimiters, t)
					if len(delimiters) > maxSTSNesting {
						return stsError("interpolation nesting exceeds limit")
					}
				case "}", "]", ")":
					open := delimiters[len(delimiters)-1]
					if open == "{" && t != "}" || open == "[" && t != "]" || open == "(" && t != ")" {
						return stsError("unbalanced interpolation")
					}
					delimiters = delimiters[:len(delimiters)-1]
				}
			}
		}
	}
	return stsError("unterminated template")
}

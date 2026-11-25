// Package curlparse turns a curl command line (as produced by browser DevTools
// "Copy as cURL") into a plain request description.
//
// Only the subset DevTools emits is supported. Any flag outside that subset is
// reported in Request.Warnings instead of being silently dropped, so the caller
// can tell the user that the probe may differ from what the curl would send.
package curlparse

import (
	"errors"
	"fmt"
	"maps"
	"strings"
)

// Request is the parsed result. Treat it as read-only; use HeadersCopy when a
// mutable map is needed.
type Request struct {
	Method   string
	URL      string
	Headers  map[string]string
	Body     []byte
	Warnings []string
}

// HeadersCopy returns an independent copy of the headers (never nil).
func (r Request) HeadersCopy() map[string]string {
	out := make(map[string]string, len(r.Headers))
	maps.Copy(out, r.Headers)
	return out
}

// flagKind describes how a recognised curl flag is handled.
type flagKind int

const (
	flagMethod flagKind = iota
	flagHeader
	flagData
	flagURL
	flagCookie
	flagIgnored // boolean flag with no effect on the request we send
)

var knownFlags = map[string]flagKind{
	"-X": flagMethod, "--request": flagMethod,
	"-H": flagHeader, "--header": flagHeader,
	"-d": flagData, "--data": flagData, "--data-raw": flagData,
	"--data-binary": flagData, "--data-ascii": flagData,
	"--url": flagURL,
	"-b":    flagCookie, "--cookie": flagCookie,
	"--compressed": flagIgnored,
	"-s":           flagIgnored, "--silent": flagIgnored,
	"-k": flagIgnored, "--insecure": flagIgnored,
	"-L": flagIgnored, "--location": flagIgnored,
	"-i": flagIgnored, "--include": flagIgnored,
}

// ignorableShortCluster lists the short boolean flags that may be bundled
// (e.g. "-sLk").
const ignorableShortCluster = "skLi"

// builder accumulates parse state; it is local to one Parse call.
type builder struct {
	method   string
	url      string
	headers  map[string]string
	data     []string
	hasData  bool
	warnings []string
}

// Parse parses a curl command line.
func Parse(input string) (Request, error) {
	if strings.TrimSpace(input) == "" {
		return Request{}, errors.New("curl 指令是空的")
	}
	tokens, err := tokenize(input)
	if err != nil {
		return Request{}, err
	}
	if len(tokens) == 0 || tokens[0] != "curl" {
		return Request{}, errors.New("不是 curl 指令：必須以 curl 開頭")
	}

	b := &builder{headers: map[string]string{}}
	if err := b.consume(tokens[1:]); err != nil {
		return Request{}, err
	}
	return b.build()
}

func (b *builder) consume(tokens []string) error {
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			b.addPositional(tok)
			continue
		}

		name, value, hasValue := splitFlag(tok)
		kind, known := knownFlags[name]
		if !known {
			if isIgnorableCluster(tok) {
				continue
			}
			b.warnings = append(b.warnings, fmt.Sprintf("不支援的 curl 旗標 %s，已略過（探測送出的請求可能與原指令不同）", tok))
			continue
		}
		if kind == flagIgnored {
			continue
		}
		if !hasValue {
			if i+1 >= len(tokens) {
				return fmt.Errorf("curl 旗標 %s 缺少值", name)
			}
			i++
			value = tokens[i]
		}
		if err := b.apply(kind, value); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) addPositional(tok string) {
	if b.url == "" {
		b.url = tok
		return
	}
	b.warnings = append(b.warnings, fmt.Sprintf("多出的參數 %q，已略過（只用第一個網址）", tok))
}

func (b *builder) apply(kind flagKind, value string) error {
	switch kind {
	case flagMethod:
		b.method = strings.ToUpper(value)
	case flagHeader:
		name, val, ok := strings.Cut(value, ":")
		if !ok || strings.TrimSpace(name) == "" {
			b.warnings = append(b.warnings, fmt.Sprintf("看不懂的 header %q，已略過", value))
			return nil
		}
		b.headers[strings.TrimSpace(name)] = strings.TrimSpace(val)
	case flagData:
		b.data = append(b.data, value)
		b.hasData = true
	case flagURL:
		b.addPositional(value)
	case flagCookie:
		b.headers["Cookie"] = value
	}
	return nil
}

func (b *builder) build() (Request, error) {
	if b.url == "" {
		return Request{}, errors.New("curl 指令裡找不到網址")
	}
	method := b.method
	if method == "" {
		method = "GET"
		if b.hasData {
			method = "POST" // curl's own rule: -d without -X sends POST
		}
	}
	var body []byte
	if b.hasData {
		body = []byte(strings.Join(b.data, "&"))
	}
	var headers map[string]string
	if len(b.headers) > 0 {
		headers = b.headers
	}
	return Request{
		Method:   method,
		URL:      b.url,
		Headers:  headers,
		Body:     body,
		Warnings: b.warnings,
	}, nil
}

// splitFlag separates the flag name from an attached value:
// "--header=x" → ("--header", "x", true); "-XPOST" → ("-X", "POST", true).
func splitFlag(tok string) (name, value string, hasValue bool) {
	if strings.HasPrefix(tok, "--") {
		if n, v, ok := strings.Cut(tok, "="); ok {
			return n, v, true
		}
		return tok, "", false
	}
	if len(tok) > 2 {
		short := tok[:2]
		if k, ok := knownFlags[short]; ok && k != flagIgnored {
			return short, tok[2:], true
		}
	}
	return tok, "", false
}

func isIgnorableCluster(tok string) bool {
	if strings.HasPrefix(tok, "--") || len(tok) < 2 {
		return false
	}
	for _, r := range tok[1:] {
		if !strings.ContainsRune(ignorableShortCluster, r) {
			return false
		}
	}
	return true
}

// tokenize splits a shell-like command line into words. It understands the
// quoting forms DevTools emits: single quotes, double quotes, Bash ANSI-C
// quoting ($'…'), backslash escapes and backslash-newline continuations.
// It deliberately does NOT expand variables or globs.
func tokenize(s string) ([]string, error) {
	t := &tokenizer{src: []rune(s)}
	return t.run()
}

type tokenizer struct {
	src    []rune
	pos    int
	words  []string
	cur    strings.Builder
	inWord bool // distinguishes an empty quoted word ('') from no word
}

func (t *tokenizer) run() ([]string, error) {
	for t.pos < len(t.src) {
		r := t.src[t.pos]
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			t.flush()
			t.pos++
		case r == '\\':
			t.unquotedEscape()
		case r == '\'':
			if err := t.singleQuoted(); err != nil {
				return nil, err
			}
		case r == '"':
			if err := t.doubleQuoted(); err != nil {
				return nil, err
			}
		case r == '$' && t.peek(1) == '\'':
			if err := t.ansiQuoted(); err != nil {
				return nil, err
			}
		default:
			t.cur.WriteRune(r)
			t.inWord = true
			t.pos++
		}
	}
	t.flush()
	return t.words, nil
}

func (t *tokenizer) peek(offset int) rune {
	if t.pos+offset < len(t.src) {
		return t.src[t.pos+offset]
	}
	return 0
}

func (t *tokenizer) flush() {
	if t.inWord {
		t.words = append(t.words, t.cur.String())
		t.cur.Reset()
		t.inWord = false
	}
}

// unquotedEscape handles "\" outside quotes: "\<newline>" is a continuation
// (dropped), anything else yields the next character literally.
func (t *tokenizer) unquotedEscape() {
	next := t.peek(1)
	switch {
	case next == '\n':
		t.pos += 2
	case next == '\r' && t.peek(2) == '\n':
		t.pos += 3
	case next == 0:
		t.pos++ // trailing lone backslash: ignore
	default:
		t.cur.WriteRune(next)
		t.inWord = true
		t.pos += 2
	}
}

func (t *tokenizer) singleQuoted() error {
	t.pos++ // opening '
	t.inWord = true
	for t.pos < len(t.src) {
		r := t.src[t.pos]
		t.pos++
		if r == '\'' {
			return nil
		}
		t.cur.WriteRune(r)
	}
	return errors.New("單引號沒有關閉")
}

func (t *tokenizer) doubleQuoted() error {
	t.pos++ // opening "
	t.inWord = true
	for t.pos < len(t.src) {
		r := t.src[t.pos]
		switch {
		case r == '"':
			t.pos++
			return nil
		case r == '\\' && t.peek(1) == '\n':
			t.pos += 2
		case r == '\\' && strings.ContainsRune("\"\\$`", t.peek(1)):
			t.cur.WriteRune(t.peek(1))
			t.pos += 2
		default:
			t.cur.WriteRune(r)
			t.pos++
		}
	}
	return errors.New("雙引號沒有關閉")
}

func (t *tokenizer) ansiQuoted() error {
	t.pos += 2 // opening $'
	t.inWord = true
	for t.pos < len(t.src) {
		r := t.src[t.pos]
		if r == '\'' {
			t.pos++
			return nil
		}
		if r != '\\' {
			t.cur.WriteRune(r)
			t.pos++
			continue
		}
		t.ansiEscape()
	}
	return errors.New("$'…' 引號沒有關閉")
}

var ansiSimpleEscapes = map[rune]rune{
	'n': '\n', 't': '\t', 'r': '\r', '\\': '\\', '\'': '\'', '"': '"',
	'a': '\a', 'b': '\b', 'f': '\f', 'v': '\v', 'e': 0x1b, 'E': 0x1b,
}

// ansiEscape decodes one backslash sequence inside $'…' (t.pos is at "\").
func (t *tokenizer) ansiEscape() {
	next := t.peek(1)
	if v, ok := ansiSimpleEscapes[next]; ok {
		t.cur.WriteRune(v)
		t.pos += 2
		return
	}
	digits := map[rune]int{'x': 2, 'u': 4, 'U': 8}[next]
	if digits > 0 {
		if v, n := t.readHex(t.pos+2, digits); n > 0 {
			if next == 'x' {
				t.cur.WriteByte(byte(v))
			} else {
				t.cur.WriteRune(rune(v))
			}
			t.pos += 2 + n
			return
		}
	}
	// Unknown escape: keep it verbatim, like Bash does.
	t.cur.WriteRune('\\')
	t.pos++
}

// readHex reads up to max hex digits starting at from; n is how many it read.
func (t *tokenizer) readHex(from, max int) (value, n int) {
	for n < max && from+n < len(t.src) {
		d := hexValue(t.src[from+n])
		if d < 0 {
			break
		}
		value = value*16 + d
		n++
	}
	return value, n
}

func hexValue(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return -1
}

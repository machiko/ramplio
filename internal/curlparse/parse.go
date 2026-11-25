// Package curlparse turns a curl command line (as produced by browser DevTools
// "Copy as cURL") into a plain request description.
//
// Only the subset DevTools emits is supported. Any flag outside that subset is
// reported in Request.Warnings instead of being silently dropped, so the caller
// can tell the user that the probe may differ from what the curl would send.
package curlparse

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sort"
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
	// Uncertain is true when the command contained something the parser could
	// not reproduce faithfully. Callers must fail closed (gate it like a write).
	Uncertain bool
}

// HeadersCopy returns an independent copy of the headers (never nil).
func (r Request) HeadersCopy() map[string]string {
	out := make(map[string]string, len(r.Headers))
	maps.Copy(out, r.Headers)
	return out
}

// MergeHeaders returns a new map: base overlaid by override, matching names
// case-insensitively (HTTP header names are case-insensitive). Neither input
// is modified. Returns nil when both are empty.
func MergeHeaders(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(override))
	maps.Copy(out, base)
	for k, v := range override {
		for existing := range out {
			if strings.EqualFold(existing, k) {
				delete(out, existing)
			}
		}
		out[k] = v
	}
	return out
}

// flagKind describes how a recognised curl flag is handled.
type flagKind int

const (
	flagMethod flagKind = iota
	flagHeader
	flagData
	flagDataURLEncode
	flagJSON
	flagForm
	flagUpload
	flagURL
	flagCookie
	flagUser
	flagBearer
	flagUserAgent
	flagReferer
	flagGet         // boolean: send data as query string with GET
	flagHead        // boolean: HEAD request
	flagIgnored     // boolean with no effect on the request we send
	flagIgnoredArg  // takes a value that does not change the request content
	flagUnsupported // takes a value that changes how/where the request is sent
)

// takesValue reports whether the flag consumes an argument.
func (k flagKind) takesValue() bool {
	switch k {
	case flagGet, flagHead, flagIgnored:
		return false
	}
	return true
}

var knownFlags = map[string]flagKind{
	"-X": flagMethod, "--request": flagMethod,
	"-H": flagHeader, "--header": flagHeader,
	"-d": flagData, "--data": flagData, "--data-raw": flagData,
	"--data-binary": flagData, "--data-ascii": flagData,
	"--data-urlencode": flagDataURLEncode,
	"--json":           flagJSON,
	"-F":               flagForm, "--form": flagForm, "--form-string": flagForm,
	"-T": flagUpload, "--upload-file": flagUpload,
	"--url": flagURL,
	"-b":    flagCookie, "--cookie": flagCookie,
	"-u": flagUser, "--user": flagUser,
	"--oauth2-bearer": flagBearer,
	"-A":              flagUserAgent, "--user-agent": flagUserAgent,
	"-e": flagReferer, "--referer": flagReferer,
	"-G": flagGet, "--get": flagGet,
	"-I": flagHead, "--head": flagHead,

	"--compressed": flagIgnored,
	"-v":           flagIgnored, "--verbose": flagIgnored,
	"-f": flagIgnored, "--fail": flagIgnored,
	"-4": flagIgnored, "-6": flagIgnored,
	"--http1.1": flagIgnored, "--http2": flagIgnored, "--http2-prior-knowledge": flagIgnored,
	"-s": flagIgnored, "--silent": flagIgnored,
	"-S": flagIgnored, "--show-error": flagIgnored,
	"-k": flagIgnored, "--insecure": flagIgnored,
	"-L": flagIgnored, "--location": flagIgnored,
	"-i": flagIgnored, "--include": flagIgnored,

	"-o": flagIgnoredArg, "--output": flagIgnoredArg,
	"-m": flagIgnoredArg, "--max-time": flagIgnoredArg,
	"--connect-timeout": flagIgnoredArg,
	"-w":                flagIgnoredArg, "--write-out": flagIgnoredArg,
	"-c": flagIgnoredArg, "--cookie-jar": flagIgnoredArg,
	"-D": flagIgnoredArg, "--dump-header": flagIgnoredArg,
	"--retry": flagIgnoredArg, "--retry-delay": flagIgnoredArg, "--retry-max-time": flagIgnoredArg,
	"--limit-rate": flagIgnoredArg, "--stderr": flagIgnoredArg,
	"--trace": flagIgnoredArg, "--trace-ascii": flagIgnoredArg,
	"--cacert": flagIgnoredArg, "--capath": flagIgnoredArg,
	"-E": flagIgnoredArg, "--cert": flagIgnoredArg, "--key": flagIgnoredArg,
	"-r": flagIgnoredArg, "--range": flagIgnoredArg,

	"-x": flagUnsupported, "--proxy": flagUnsupported,
	"-U": flagUnsupported, "--proxy-user": flagUnsupported,
	"--resolve": flagUnsupported, "--connect-to": flagUnsupported,
	"--unix-socket": flagUnsupported,
	"-K":            flagUnsupported, "--config": flagUnsupported,
}

// builder accumulates parse state; it is local to one Parse call.
// Warnings must never include a flag's value (it may be a credential): only
// flag names, argument positions and header names.
type builder struct {
	method      string
	url         string
	positionals int
	headerFlags int
	headers     map[string]string
	data        []string
	hasData     bool
	hasJSON     bool
	hasForm     bool
	hasUpload   bool
	get         bool
	head        bool
	uncertain   bool
	warnings    []string
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
	if len(tokens) == 0 || !strings.EqualFold(tokens[0], "curl") {
		return Request{}, errors.New("不是 curl 指令：必須以 curl 開頭")
	}

	b := &builder{}
	if err := b.consume(tokens[1:]); err != nil {
		return Request{}, err
	}
	return b.build()
}

func (b *builder) warn(format string, args ...any) {
	b.warnings = append(b.warnings, fmt.Sprintf(format, args...))
}

func (b *builder) consume(tokens []string) error {
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		var err error
		switch {
		case tok == "-" || !strings.HasPrefix(tok, "-"):
			b.addPositional(tok)
		case strings.HasPrefix(tok, "--"):
			i, err = b.longFlag(tokens, i)
		default:
			i, err = b.shortCluster(tokens, i)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// longFlag handles "--name", "--name value" and "--name=value".
// Returns the index of the last token consumed.
func (b *builder) longFlag(tokens []string, i int) (int, error) {
	name, value, attached := strings.Cut(tokens[i], "=")
	kind, known := knownFlags[name]
	if !known {
		b.unknownFlag(name)
		return i, nil
	}
	if !kind.takesValue() {
		b.applyBool(kind)
		return i, nil
	}
	if !attached {
		if i+1 >= len(tokens) {
			return i, fmt.Errorf("curl 旗標 %s 缺少值", name)
		}
		i++
		value = tokens[i]
	}
	b.apply(kind, name, value)
	return i, nil
}

// shortCluster parses bundled short flags character by character, e.g.
// "-sXPOST", "-sH value", "-skd value". The first value-taking flag consumes
// the rest of the token, or the next token when nothing is left.
func (b *builder) shortCluster(tokens []string, i int) (int, error) {
	chars := []rune(tokens[i][1:])
	for j, c := range chars {
		name := "-" + string(c)
		kind, known := knownFlags[name]
		if !known {
			b.unknownFlag(name)
			continue
		}
		if !kind.takesValue() {
			b.applyBool(kind)
			continue
		}
		value := string(chars[j+1:])
		if value == "" {
			if i+1 >= len(tokens) {
				return i, fmt.Errorf("curl 旗標 %s 缺少值", name)
			}
			i++
			value = tokens[i]
		}
		b.apply(kind, name, value)
		return i, nil
	}
	return i, nil
}

// unknownFlag records an unrecognised flag. We cannot know whether it takes a
// value or changes the request, so the result is marked uncertain (fail closed).
func (b *builder) unknownFlag(name string) {
	b.uncertain = true
	b.warn("不支援的 curl 旗標 %s，已略過（送出的請求可能與原指令不同）", name)
}

func (b *builder) addPositional(tok string) {
	b.positionals++
	if b.url == "" {
		b.url = tok
		return
	}
	b.warn("多出第 %d 個網址參數，已略過（只用第一個網址）", b.positionals)
}

func (b *builder) setHeader(name, value string) {
	b.headers = MergeHeaders(b.headers, map[string]string{name: value})
}

func (b *builder) applyBool(kind flagKind) {
	switch kind {
	case flagGet:
		b.get = true
	case flagHead:
		b.head = true
	}
}

func (b *builder) apply(kind flagKind, name, value string) {
	switch kind {
	case flagMethod:
		b.method = strings.ToUpper(value)
	case flagHeader:
		b.applyHeader(name, value)
	case flagData:
		if strings.HasPrefix(value, "@") && name != "--data-raw" {
			b.uncertain = true
			b.warn("%s @檔案 的內容不會被讀取（送出的 body 與原指令不同）", name)
		}
		b.addData(value)
	case flagDataURLEncode:
		b.addData(b.urlEncodeData(name, value))
	case flagJSON:
		b.hasJSON = true
		b.addData(value)
	case flagForm:
		b.hasForm = true
		b.uncertain = true
		b.warn("%s 的 multipart 表單無法重現，body 不會送出", name)
	case flagUpload:
		b.hasUpload = true
		b.uncertain = true
		b.warn("%s 上傳的檔案內容不會被讀取，body 不會送出", name)
	case flagURL:
		b.addPositional(value)
	case flagCookie:
		if !strings.Contains(value, "=") {
			b.warn("%s 讀取 cookie 檔不支援，已略過", name)
			return
		}
		b.setHeader("Cookie", value)
	case flagUser:
		if !strings.Contains(value, ":") {
			b.warn("%s 沒有附密碼（curl 會互動詢問），以空密碼送出", name)
			value += ":"
		}
		b.setHeader("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(value)))
	case flagBearer:
		b.setHeader("Authorization", "Bearer "+value)
	case flagUserAgent:
		b.setHeader("User-Agent", value)
	case flagReferer:
		b.setHeader("Referer", value)
	case flagIgnoredArg:
		b.warn("curl 旗標 %s 不影響送出的請求，已略過", name)
	case flagUnsupported:
		b.uncertain = true
		b.warn("curl 旗標 %s 會改變請求的送達方式，ramplio 無法重現", name)
	}
}

func (b *builder) applyHeader(flag, value string) {
	b.headerFlags++
	name, val, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(name) == "" || strings.ContainsAny(strings.TrimSpace(name), " \t") {
		b.warn("第 %d 個 %s 格式不對（應為「名稱: 值」），已略過", b.headerFlags, flag)
		return
	}
	b.setHeader(strings.TrimSpace(name), strings.TrimSpace(val))
}

func (b *builder) addData(v string) {
	b.data = append(b.data, v)
	b.hasData = true
}

// urlEncodeData mirrors curl's --data-urlencode forms: "content", "=content",
// "name=content". The "@file" / "name@file" forms read a file, which we can't.
func (b *builder) urlEncodeData(flag, v string) string {
	eq := strings.IndexByte(v, '=')
	at := strings.IndexByte(v, '@')
	switch {
	case at >= 0 && (eq < 0 || at < eq):
		b.uncertain = true
		b.warn("%s @檔案 的內容不會被讀取（送出的 body 與原指令不同）", flag)
		return v
	case eq < 0:
		return escapeData(v)
	case eq == 0:
		return escapeData(v[1:])
	default:
		return v[:eq] + "=" + escapeData(v[eq+1:])
	}
}

// escapeData percent-encodes like curl (space → %20, not "+").
func escapeData(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func (b *builder) build() (Request, error) {
	if b.url == "" {
		return Request{}, errors.New("curl 指令裡找不到網址")
	}
	reqURL := b.url
	var body []byte
	sep := "&"
	if b.hasJSON {
		sep = "" // curl concatenates repeated --json as-is
	}
	if b.hasData {
		joined := strings.Join(b.data, sep)
		if b.get {
			reqURL = appendQuery(reqURL, joined)
		} else {
			body = []byte(joined)
		}
	}
	headers := b.headers
	for _, name := range MethodOverrideHeaderNames(headers) {
		b.uncertain = true
		b.warn("header %s 會讓伺服器改用其他方法處理（可能是寫入）", name)
	}
	if b.hasJSON {
		headers = withDefaultHeader(headers, "Content-Type", "application/json")
		headers = withDefaultHeader(headers, "Accept", "application/json")
	}
	return Request{
		Method:    b.resolveMethod(),
		URL:       reqURL,
		Headers:   headers,
		Body:      body,
		Warnings:  b.warnings,
		Uncertain: b.uncertain,
	}, nil
}

// resolveMethod applies curl's precedence: -X, then -I, -G, -T, then any body.
func (b *builder) resolveMethod() string {
	switch {
	case b.method != "":
		return b.method
	case b.head:
		return "HEAD"
	case b.get:
		return "GET"
	case b.hasUpload:
		return "PUT"
	case b.hasData || b.hasForm:
		return "POST"
	}
	return "GET"
}

func appendQuery(rawURL, query string) string {
	switch {
	case !strings.Contains(rawURL, "?"):
		return rawURL + "?" + query
	case strings.HasSuffix(rawURL, "?") || strings.HasSuffix(rawURL, "&"):
		return rawURL + query
	}
	return rawURL + "&" + query
}

// withDefaultHeader sets name only when no header of that name (any case) exists.
func withDefaultHeader(h map[string]string, name, value string) map[string]string {
	for k := range h {
		if strings.EqualFold(k, name) {
			return h
		}
	}
	return MergeHeaders(h, map[string]string{name: value})
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

// methodOverrideHeaders make many frameworks treat a request as another
// method (e.g. a GET carrying X-HTTP-Method-Override: DELETE).
var methodOverrideHeaders = []string{"X-HTTP-Method-Override", "X-Method-Override", "X-HTTP-Method"}

// HasMethodOverride reports whether h asks the server to use another method.
func HasMethodOverride(h map[string]string) bool {
	return len(MethodOverrideHeaderNames(h)) > 0
}

// MethodOverrideHeaderNames returns the method-override header names present
// in h (as spelled in h), sorted for stable output.
func MethodOverrideHeaderNames(h map[string]string) []string {
	var names []string
	for k := range h {
		for _, o := range methodOverrideHeaders {
			if strings.EqualFold(k, o) {
				names = append(names, k)
			}
		}
	}
	sort.Strings(names)
	return names
}

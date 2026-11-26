package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"gopkg.in/yaml.v3"
)

const (
	headerEnvPrefix = "RAMPLIO_H_"
	queryEnvPrefix  = "RAMPLIO_Q_"

	// The saved scenario ramps to the safe limit, then holds it long enough
	// for a regression to show up in p99 and the error rate.
	savedRampDuration = "30s"
	savedHoldDuration = "2m"
	// savedErrorRatePct matches discover's PASS rule (error rate under 1%), so
	// "discover said safe" and "the saved scenario passes" mean the same thing.
	savedErrorRatePct = 1.0
	savedStepName     = "探測目標"
	// Saved files may carry a plaintext body; keep them private to the owner.
	savedScenarioPerm = 0o600
	// templateMarker starts a template token; run renders URL, headers and
	// body, so a literal one in the saved request would break every request.
	templateMarker = "{{"

	unsetEnvNote    = "這些環境變數沒設時會送出空值"
	literalPathNote = "網址路徑照原文保存，若路徑含秘密請手動改寫"
)

// sensitiveHeaderExtraParts adds what the shared query keyword list does not
// cover; everything else (auth, token, key, secret, session…) is reused from
// sensitiveQueryKeyParts so there is one keyword list to maintain.
var sensitiveHeaderExtraParts = []string{"cookie"}

func isSensitiveHeaderName(name string) bool {
	if isSensitiveQueryKey(name) {
		return true
	}
	lower := strings.ToLower(name)
	for _, part := range sensitiveHeaderExtraParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// scenarioEnvName turns a header or query name into an environment variable
// name: upper case, anything outside A-Z and 0-9 becomes "_".
func scenarioEnvName(prefix, name string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, name)
	return prefix + mapped
}

func envRef(name string) string { return `{{env "` + name + `"}}` }

// envBindings collects the variables a saved scenario needs. Two different
// values mapping to the same variable name would silently share one value, so
// that is an error.
type envBindings map[string]string

func (b envBindings) bind(name, value string) error {
	if prev, ok := b[name]; ok && prev != value {
		return fmt.Errorf("有兩個不同的敏感值都會對應到環境變數 %s，無法分開保存，未產生情境檔", name)
	}
	b[name] = value
	return nil
}

func (b envBindings) names() []string {
	out := make([]string, 0, len(b))
	for name := range b {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// checkSaveScenarioTarget rejects what can be rejected before probing, so the
// user does not wait through a full probe only to be told the file exists.
func checkSaveScenarioTarget(path string, req protocols.Request, tolerance time.Duration) error {
	u, err := url.Parse(req.URL)
	if err != nil {
		return errors.New("網址無法解析，未產生情境檔")
	}
	if u.User != nil {
		return errors.New(`網址含帳號密碼（user:pass@），情境檔無法安全保存；請改用 -H "Authorization: Basic …" 帶憑證，未產生情境檔`)
	}
	if dirErr := checkParentDir(path); dirErr != nil {
		return dirErr
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		return fmt.Errorf("%s 已存在，不覆蓋；請換一個檔名或先自行移走", path)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("無法檢查 %s：%w", path, statErr)
	}
	// Everything that does not depend on the probe result (template markers,
	// env name collisions, the p99 threshold) is checked now with a stand-in
	// load, so the user is not made to wait through the probe first.
	_, _, buildErr := buildSavedScenario(req, 1, tolerance)
	return buildErr
}

func checkParentDir(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s 的上層目錄 %s 不存在，請先建立目錄，未產生情境檔", path, dir)
		}
		return fmt.Errorf("無法檢查 %s 的上層目錄：%w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s 的上層 %s 不是目錄，未產生情境檔", path, dir)
	}
	return nil
}

// checkNoTemplateMarkers rejects literal "{{" that would stay in the saved
// file. Sensitive values are already replaced by env references, which are
// removed before the check, so only text kept verbatim is inspected. The
// message names the part, never the value (it may be a secret).
func checkNoTemplateMarkers(stepURL string, headers map[string]string, body []byte, envNames []string) error {
	const hint = "run 會把它當成模板解析而失敗，請改掉後再存或手動寫情境檔，未產生情境檔"
	stripped := stepURL
	for _, name := range envNames {
		stripped = strings.ReplaceAll(stripped, envRef(name), "")
	}
	if strings.Contains(stripped, templateMarker) {
		return fmt.Errorf("網址含 %s，%s", templateMarker, hint)
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := headers[name]
		if isSensitiveHeaderName(name) {
			continue // replaced by an env reference
		}
		if strings.Contains(value, templateMarker) {
			return fmt.Errorf("header %s 的值含 %s，%s", name, templateMarker, hint)
		}
	}
	if bytes.Contains(body, []byte(templateMarker)) {
		return fmt.Errorf("body 含 %s，%s", templateMarker, hint)
	}
	return nil
}

// envizeURL drops the fragment (never sent to the server) and replaces the
// values of secret-looking query parameters with env references. The rest of
// the URL is kept byte for byte.
func envizeURL(raw string, env envBindings) (string, error) {
	noFragment, _, _ := strings.Cut(raw, "#")
	base, rawQuery, hasQuery := strings.Cut(noFragment, "?")
	if !hasQuery || rawQuery == "" {
		return noFragment, nil
	}
	parts := strings.Split(rawQuery, "&")
	out := make([]string, len(parts))
	for i, part := range parts {
		k, v, hasValue := strings.Cut(part, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if !hasValue || !isSensitiveQueryKey(name) {
			out[i] = part
			continue
		}
		envName := scenarioEnvName(queryEnvPrefix, name)
		if err := env.bind(envName, v); err != nil {
			return "", err
		}
		out[i] = k + "=" + envRef(envName)
	}
	return base + "?" + strings.Join(out, "&"), nil
}

func envizeHeaders(headers map[string]string, env envBindings) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		if !isSensitiveHeaderName(name) {
			out[name] = value
			continue
		}
		envName := scenarioEnvName(headerEnvPrefix, name)
		if err := env.bind(envName, value); err != nil {
			return nil, err
		}
		out[name] = envRef(envName)
	}
	return out, nil
}

// buildSavedScenario turns a finished probe into a scenario that holds the
// safe limit and fails when p99 or the error rate exceed discover's PASS rule.
func buildSavedScenario(req protocols.Request, safeLimit int, tolerance time.Duration) (scenarios.Scenario, []string, error) {
	env := envBindings{}
	stepURL, err := envizeURL(req.URL, env)
	if err != nil {
		return scenarios.Scenario{}, nil, err
	}
	headers, err := envizeHeaders(req.Headers, env)
	if err != nil {
		return scenarios.Scenario{}, nil, err
	}
	envNames := env.names()
	if err := checkNoTemplateMarkers(stepURL, req.Headers, req.Body, envNames); err != nil {
		return scenarios.Scenario{}, nil, err
	}
	if tolerance < time.Millisecond {
		return scenarios.Scenario{}, nil, fmt.Errorf("--tolerance %s 不到 1ms，換算成 p99_ms 門檻會是 0，未產生情境檔", tolerance)
	}
	p99 := float64(tolerance.Milliseconds())
	errRate := savedErrorRatePct
	sc := scenarios.Scenario{
		Name: fmt.Sprintf("discover 安全上限每秒 %d 個請求", safeLimit),
		Stages: []scenarios.Stage{
			{DurationRaw: savedRampDuration, TargetRPS: safeLimit},
			{DurationRaw: savedHoldDuration, TargetRPS: safeLimit},
		},
		Steps: []scenarios.Step{{
			Name:    savedStepName,
			Method:  req.Method,
			URL:     stepURL,
			Headers: headers,
			Body:    string(req.Body),
		}},
		Thresholds: &scenarios.Thresholds{P99Ms: &p99, ErrorRatePct: &errRate},
	}
	return sc, envNames, nil
}

// renderSavedScenario marshals the scenario with a header comment and checks
// that the result parses and validates, so a broken file is never written.
func renderSavedScenario(sc scenarios.Scenario, envNames []string) ([]byte, error) {
	body, err := yaml.Marshal(sc)
	if err != nil {
		return nil, fmt.Errorf("產生情境檔失敗：%w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("# 由 ramplio discover --save-scenario 產生。\n")
	if len(envNames) > 0 {
		buf.WriteString("# 憑證不存在檔案裡，執行前先設定環境變數（值是原本完整的 header／參數值）：\n")
		for _, name := range envNames {
			buf.WriteString("#   export " + name + "=…\n")
		}
		buf.WriteString("# " + unsetEnvNote + "，請求會帶著空的 header／參數送出。\n")
	}
	buf.Write(body)
	if _, err := scenarios.Parse(bytes.NewReader(buf.Bytes())); err != nil {
		return nil, fmt.Errorf("產生的情境檔沒有通過驗證：%w", err)
	}
	return buf.Bytes(), nil
}

// writeNewFile creates path exclusively: an existing file is never replaced,
// even if it appeared after the pre-probe check.
func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, savedScenarioPerm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s 已存在，不覆蓋；請換一個檔名或先自行移走", path)
		}
		return fmt.Errorf("寫入 %s 失敗：%w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("寫入 %s 失敗：%w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("寫入 %s 失敗：%w", path, err)
	}
	return nil
}

// saveScenarioFromProbe writes the scenario after a probe, or explains why not.
// Interrupted or zero-capacity probes are refused: a scenario built from them
// would encode a load the service was never shown to handle.
func saveScenarioFromProbe(w io.Writer, interrupted bool, opts discoverOptions, result discover.DiscoverResult) error {
	if interrupted {
		return errors.New("探測被中斷，結果不完整，未產生情境檔")
	}
	if result.SafeLimit <= 0 {
		return errors.New("探測的安全上限為 0（最低等級就沒通過），沒有可守的負載，未產生情境檔")
	}
	sc, envNames, err := buildSavedScenario(opts.Request, result.SafeLimit, opts.Tolerance)
	if err != nil {
		return err
	}
	data, err := renderSavedScenario(sc, envNames)
	if err != nil {
		return err
	}
	if err := writeNewFile(opts.ScenarioFile, data); err != nil {
		return err
	}
	printSavedScenario(w, opts, result.SafeLimit, envNames)
	return nil
}

func printSavedScenario(w io.Writer, opts discoverOptions, safeLimit int, envNames []string) {
	fmt.Fprintf(w, "\n  ✓ 已存成情境檔：%s\n", opts.ScenarioFile)
	fmt.Fprintf(w, "    負載：%s 內爬升到每秒 %d 個請求，維持 %s\n", savedRampDuration, safeLimit, savedHoldDuration)
	fmt.Fprintf(w, "    門檻：p99 超過 %dms 或錯誤率超過 %.0f%% 就判定失敗（exit 1）\n",
		opts.Tolerance.Milliseconds(), savedErrorRatePct)
	if len(envNames) > 0 {
		fmt.Fprintln(w, "    憑證沒有存進檔案，執行前先設定（值是原本完整的 header／參數值，這裡不顯示；"+unsetEnvNote+"）：")
		for _, name := range envNames {
			fmt.Fprintf(w, "      export %s=…\n", name)
		}
	}
	if hasNonRootPath(opts.Request.URL) {
		fmt.Fprintln(w, "    ⚠ "+literalPathNote+"。")
	}
	if len(opts.Request.Body) > 0 {
		fmt.Fprintln(w, "    ⚠ body 以明文存進檔案，commit 前請確認裡面沒有密碼或 token。")
	}
	fmt.Fprintf(w, "    重跑：ramplio run --scenario %s\n", opts.ScenarioFile)
}

// hasNonRootPath reports whether the URL has a path beyond "/", which is saved
// verbatim and may itself carry an ID or a secret.
func hasNonRootPath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Path != "" && u.Path != "/"
}

// printSaveScenarioHint points a successful probe at --save-scenario. It names
// no URL or header, so nothing secret can leak through it.
func printSaveScenarioHint(w io.Writer) {
	fmt.Fprintln(w, "\n  想把這次結果存成可重跑、退步就 fail 的情境檔：在 ramplio discover 指令加上 --save-scenario scenario.yaml")
}

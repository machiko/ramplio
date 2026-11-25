package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/curlparse"
	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/machiko/ramplio/v3/internal/history"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/spf13/cobra"
)

// errWriteNotAllowed marks a run that stopped after the single preflight
// because the method may change data and --allow-write was not given.
var errWriteNotAllowed = errors.New("寫入型方法需要加 --allow-write 才會開始探測")

// discoverOptions is everything runDiscover needs; shared by `discover` and the
// root smart entry so both go through the same write-safety gate.
type discoverOptions struct {
	Request       protocols.Request
	Tolerance     time.Duration
	MaxRPS        int
	ProbeDuration time.Duration
	BaselineFile  string
	AllowWrite    bool
	// FromCurl switches the --allow-write hint to the stdin form so the curl
	// (and any token in it) is never echoed back to the terminal.
	FromCurl bool
	// Uncertain means the curl had parts we could not reproduce; the request
	// is gated like a write (fail closed) until --allow-write is given.
	Uncertain bool
	// NoHistory skips the automatic run record and the comparison with the previous run.
	NoHistory bool
}

// resolvedTarget is the request discover will send plus what the curl parser
// had to say about it.
type resolvedTarget struct {
	Request   protocols.Request
	Warnings  []string
	Uncertain bool
}

const (
	defaultDiscoverTolerance     = 2 * time.Second
	defaultDiscoverMaxRPS        = 500
	defaultDiscoverProbeDuration = 15 * time.Second
)

func newDiscoverCmd() *cobra.Command {
	var (
		url           string
		curlCmd       string
		method        string
		body          string
		headerFlags   []string
		allowWrite    bool
		tolerance     string
		maxRPS        int
		probeDuration string
		baselineFile  string
		noHistory     bool
	)

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "給網址，自動找出你的服務能撐多少人",
		Long: `對目標以遞增的請求速率自動探測，找出容量上限。不需要任何壓測知識。

當你想回答「我的服務撐得住多少流量？」時，就跑這個。
需要登入或送資料的 API，可用 -H／--body，或直接貼瀏覽器 DevTools 的「Copy as cURL」給 --curl。
POST／PUT／PATCH／DELETE 等會改資料的方法，預設只送一發預檢，確定要壓測請加 --allow-write。`,
		Example: `  ramplio discover --url https://example.com
  ramplio discover --url https://api.example.com/me -H "Authorization: Bearer $TOKEN"
  pbpaste | ramplio discover --curl -
  ramplio discover --url https://example.com --max-rps 300 --probe-duration 30s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tol, err := time.ParseDuration(tolerance)
			if err != nil {
				return fmt.Errorf("invalid --tolerance %q: %w", tolerance, err)
			}
			pd, err := time.ParseDuration(probeDuration)
			if err != nil {
				return fmt.Errorf("invalid --probe-duration %q: %w", probeDuration, err)
			}
			target, err := resolveDiscoverRequest(cmd, url, curlCmd, method, body, headerFlags)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			w := cmd.OutOrStdout()
			printCurlWarnings(w, target.Warnings)
			return runDiscover(ctx, w, cmd.ErrOrStderr(), discoverOptions{
				Request:       target.Request,
				Uncertain:     target.Uncertain,
				Tolerance:     tol,
				MaxRPS:        maxRPS,
				ProbeDuration: pd,
				BaselineFile:  baselineFile,
				AllowWrite:    allowWrite,
				FromCurl:      curlCmd != "",
				NoHistory:     noHistory,
			})
		},
	}

	cmd.Flags().StringVarP(&url, "url", "u", "", "Target URL（與 --curl 二擇一）")
	cmd.Flags().StringVar(&curlCmd, "curl", "", "貼上 DevTools「Copy as cURL」的整段指令；給 - 則從 stdin 讀")
	cmd.Flags().StringVar(&method, "method", "", "HTTP 方法（預設 GET；有 --body 時預設 POST）")
	cmd.Flags().StringVar(&body, "body", "", "請求內容")
	cmd.Flags().StringArrayVarP(&headerFlags, "header", "H", nil, `額外的 header，可重複，例：-H "Authorization: Bearer xxx"`)
	cmd.Flags().BoolVar(&allowWrite, "allow-write", false, "允許對 POST/PUT/PATCH/DELETE 等會改資料的方法做壓測")
	cmd.Flags().StringVar(&tolerance, "tolerance", defaultDiscoverTolerance.String(), "Acceptable p99 response time (e.g. 1s, 500ms, 2s)")
	cmd.Flags().IntVar(&maxRPS, "max-rps", defaultDiscoverMaxRPS, "Stop probing above this request rate")
	cmd.Flags().StringVar(&probeDuration, "probe-duration", defaultDiscoverProbeDuration.String(), "Duration of each probe (e.g. 10s, 30s)")
	cmd.Flags().StringVar(&baselineFile, "save-baseline", "", "把容量結果存成 baseline 快照(供 ramplio compare 守門)")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "不自動把這次結果存進 .ramplio/ 執行紀錄，也不跟上一次比較")

	return cmd
}

// resolveDiscoverRequest builds the request from --url or --curl, then lets
// explicitly given -H/--method/--body override what the curl said.
func resolveDiscoverRequest(cmd *cobra.Command, url, curlCmd, method, body string, headerFlags []string) (resolvedTarget, error) {
	if (url == "") == (curlCmd == "") {
		return resolvedTarget{}, errors.New("--url 與 --curl 二擇一，必須剛好給一個")
	}
	explicit, err := parseHeaderFlags(headerFlags)
	if err != nil {
		return resolvedTarget{}, err
	}

	base := resolvedTarget{Request: protocols.Request{URL: url}}
	if curlCmd != "" {
		parsed, perr := readCurlArg(cmd.InOrStdin(), curlCmd)
		if perr != nil {
			return resolvedTarget{}, perr
		}
		base = targetFromCurl(parsed)
	}

	req := protocols.Request{
		Method:  base.Request.Method,
		URL:     base.Request.URL,
		Headers: curlparse.MergeHeaders(base.Request.Headers, explicit),
		Body:    base.Request.Body,
	}
	if cmd.Flags().Changed("body") {
		req.Body = []byte(body)
	}
	switch {
	case method != "":
		req.Method = strings.ToUpper(method)
	case req.Method == "" && len(req.Body) > 0:
		req.Method = "POST" // same rule as curl -d without -X
	case req.Method == "":
		req.Method = "GET"
	}
	resolved := resolvedTarget{Request: req, Warnings: base.Warnings, Uncertain: base.Uncertain}
	// The curl parser already flags override headers it saw; explicit -H
	// flags are checked here so `--url … -H "X-HTTP-Method-Override: DELETE"`
	// is gated too.
	for _, name := range curlparse.MethodOverrideHeaderNames(explicit) {
		resolved.Uncertain = true
		resolved.Warnings = append(resolved.Warnings, fmt.Sprintf("header %s 會讓伺服器改用其他方法處理（可能是寫入）", name))
	}
	return resolved, nil
}

func targetFromCurl(parsed curlparse.Request) resolvedTarget {
	return resolvedTarget{
		Request:   protocols.Request{Method: parsed.Method, URL: parsed.URL, Headers: parsed.HeadersCopy(), Body: parsed.Body},
		Warnings:  parsed.Warnings,
		Uncertain: parsed.Uncertain,
	}
}

// readCurlArg parses the --curl value, reading it from stdin when it is "-".
func readCurlArg(stdin io.Reader, value string) (curlparse.Request, error) {
	text := value
	if value == "-" {
		in, err := readLimitedInput(stdin)
		if err != nil {
			return curlparse.Request{}, err
		}
		text = in
	}
	parsed, err := curlparse.Parse(strings.TrimSpace(text))
	if err != nil {
		return curlparse.Request{}, fmt.Errorf("解析 curl 失敗：%w", err)
	}
	return parsed, nil
}

// maxCurlInputBytes caps stdin so a mistaken pipe can't eat unbounded memory.
const maxCurlInputBytes = 1 << 20

// readLimitedInput reads stdin up to maxCurlInputBytes. Larger input is an
// explicit error: silently truncating a curl could drop headers or flags.
func readLimitedInput(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxCurlInputBytes+1))
	if err != nil {
		return "", fmt.Errorf("從 stdin 讀取失敗：%w", err)
	}
	if len(b) > maxCurlInputBytes {
		return "", fmt.Errorf("輸入超過 1MiB（上限 %d bytes）：請確認貼上的是單一則 curl", maxCurlInputBytes)
	}
	return string(b), nil
}

// parseHeaderFlags turns repeated -H values into a map. Names are de-duplicated
// case-insensitively and a later -H overrides an earlier one.
func parseHeaderFlags(values []string) (map[string]string, error) {
	var out map[string]string
	for i, v := range values {
		name, val, ok := strings.Cut(v, ":")
		if !ok || strings.TrimSpace(name) == "" {
			// Never echo the value: a malformed header is often a token.
			return nil, fmt.Errorf("第 %d 個 -H 格式不對，應為 \"名稱: 值\"", i+1)
		}
		out = curlparse.MergeHeaders(out, map[string]string{strings.TrimSpace(name): strings.TrimSpace(val)})
	}
	return out, nil
}

// isSafeMethod reports whether a method is read-only by HTTP semantics and can
// be load tested without --allow-write.
func isSafeMethod(m string) bool {
	switch strings.ToUpper(m) {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// maskedHeaders lists header names with values hidden, sorted for stable
// output. Values are never printed: they are often tokens or cookies.
func maskedHeaders(h map[string]string) string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k+": ***")
	}
	sort.Strings(names)
	return strings.Join(names, "、")
}

func printCurlWarnings(w io.Writer, warnings []string) {
	for _, msg := range warnings {
		fmt.Fprintf(w, "  ⚠ %s\n", msg)
	}
}

// runDiscover prints the target, enforces the write-safety gate, runs the
// probes and prints the capacity report. A blocked write returns
// errWriteNotAllowed after exactly one preflight request.
func runDiscover(ctx context.Context, w, errW io.Writer, opts discoverOptions) error {
	req := opts.Request
	fmt.Fprintf(w, "  目標：    %s %s\n", req.Method, maskURLForDisplay(req.URL))
	if len(req.Headers) > 0 {
		fmt.Fprintf(w, "  Header：  %s\n", maskedHeaders(req.Headers))
	}
	if len(req.Body) > 0 {
		fmt.Fprintf(w, "  Body：    %d bytes\n", len(req.Body))
	}

	if (!isSafeMethod(req.Method) || opts.Uncertain) && !opts.AllowWrite {
		return blockWrite(ctx, w, opts)
	}

	probeCount := len(discover.ProbeSequence(opts.MaxRPS))
	estMinutes := float64(int(opts.ProbeDuration.Seconds())*probeCount) / 60.0
	fmt.Fprintf(w, "  容許值：  p99 < %s、錯誤率 < 1%%\n", opts.Tolerance)
	fmt.Fprintf(w, "  探測點：  最多 %d 個等級（預估 %.0f–%.0f 分鐘）\n\n",
		probeCount, estMinutes*0.5, estMinutes)
	fmt.Fprint(w, "  正在探測吞吐容量…\n\n")

	prober := discover.New(discover.Config{
		URL:           req.URL,
		Method:        req.Method,
		Headers:       req.Headers,
		Body:          req.Body,
		Tolerance:     opts.Tolerance,
		MaxRPS:        opts.MaxRPS,
		ProbeDuration: opts.ProbeDuration,
		HTTPConfig:    protocols.DefaultHTTPConfig(),
	})
	result := prober.Run(ctx, nil, func(pr discover.ProbeResult) {
		writeDiscoverProbe(w, pr)
	})

	fmt.Fprintln(w)
	writeDiscoverReport(w, result, opts.Tolerance)

	if opts.BaselineFile != "" {
		// Saved files outlive the terminal: mask credentials the same way.
		// compare only warns when two baselines' targets differ, and masking
		// is deterministic, so the same target still matches.
		b := baseline.FromDiscover(result, maskURLForDisplay(req.URL))
		b.GitCommit = currentGitCommit()
		writeBaselineFile(w, errW, opts.BaselineFile, b)
	}

	// An interrupted probe is partial: recording it would make the next run
	// look like a big change, so only completed probes are recorded.
	if !opts.NoHistory && ctx.Err() == nil {
		rec := history.Record{
			Baseline:  baseline.FromDiscover(result, maskURLForDisplay(req.URL)),
			Kind:      history.KindDiscover,
			TargetKey: discoverTargetKey(req, opts.Tolerance, opts.MaxRPS, opts.ProbeDuration),
		}
		rec.GitCommit = currentGitCommit()
		recordAndDiff(w, errW, historyDir(), rec)
	}
	return nil
}

// blockWrite sends the single preflight (with the real headers and body, so
// the user sees a meaningful status) and explains how to opt in.
func blockWrite(ctx context.Context, w io.Writer, opts discoverOptions) error {
	method := opts.Request.Method
	fmt.Fprintln(w)
	if !isSafeMethod(method) {
		fmt.Fprintf(w, "  ⚠ %s 可能會改動資料，預設不做壓測。\n", method)
	}
	if opts.Uncertain {
		fmt.Fprintln(w, "  ⚠ 這段 curl 有無法完整解讀的部分（見上方警告），送出的請求可能與原指令不同、甚至會改動資料，預設不做壓測。")
	}
	res, err := preflightOnce(ctx, w, protocols.DefaultHTTPConfig(), opts.Request)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n  %s\n", preflightOutcomeMessage(method, res))
	step := nextStep{"在原本的指令後面加上", "--allow-write"}
	if opts.FromCurl {
		step = nextStep{"把 curl 留在剪貼簿，執行", "pbpaste | ramplio discover --curl - --allow-write"}
	}
	printNextSteps(w, "\n  確定要對它壓測（會真的送出大量寫入）：", step)
	fmt.Fprintln(w)
	return errWriteNotAllowed
}

func writeDiscoverProbe(w io.Writer, pr discover.ProbeResult) {
	icon := "✓"
	switch pr.Status {
	case discover.ProbeWarn:
		icon = "⚠"
	case discover.ProbeFail:
		icon = "✗"
	}

	var p99str string
	ms := pr.P99.Milliseconds()
	if ms >= 1000 {
		p99str = fmt.Sprintf("%.1fs", float64(ms)/1000)
	} else {
		p99str = fmt.Sprintf("%dms", ms)
	}

	fmt.Fprintf(w, "  每秒 %5d 個  %s  p99=%-8s  錯誤=%.1f%%\n",
		pr.RPS, icon, p99str, pr.ErrorRate)
}

const reportWidth = 46

// displayWidth approximates the terminal column width of s: ASCII runes count as
// 1, everything else (CJK, full-width punctuation) as 2. The report box aligns on
// this rather than len() (bytes) or rune count so the right border stays straight
// when Chinese text is mixed in.
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		if r < 128 {
			width++
		} else {
			width += 2
		}
	}
	return width
}

func writeDiscoverReport(w io.Writer, result discover.DiscoverResult, tolerance time.Duration) {
	top := "  ┌" + strings.Repeat("─", reportWidth) + "┐"
	bot := "  └" + strings.Repeat("─", reportWidth) + "┘"
	sep := "  ├" + strings.Repeat("─", reportWidth) + "┤"
	row := func(s string) {
		pad := reportWidth - 2 - displayWidth(s)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(w, "  │  %s%s│\n", s, strings.Repeat(" ", pad))
	}

	fmt.Fprintln(w, top)
	row("容量報告")
	fmt.Fprintln(w, sep)

	if result.SafeLimit > 0 {
		row(fmt.Sprintf("安全上限：    每秒約 %d 個請求", result.SafeLimit))
	} else {
		row("安全上限：    每秒不到 5 個請求")
	}

	switch {
	case result.BreakingPoint > 0:
		row(fmt.Sprintf("臨界點：      每秒約 %d 個請求", result.BreakingPoint))
	case result.Exhausted:
		row("臨界點：      測試範圍內未觸及")
	default:
		row("臨界點：      測試已中斷")
	}

	fmt.Fprintln(w, sep)
	row("這代表什麼：")
	row("")

	switch {
	case result.SafeLimit == 0:
		row("你的服務在很低的流量下就吃力了。")
		row("建議先檢查伺服器健康狀態，再做壓測。")
	case result.Exhausted:
		row(fmt.Sprintf("你的服務通過了全部 %d 個測試等級。", len(result.Probes)))
		row(fmt.Sprintf("最大安全吞吐量超過每秒 %d 個請求。", result.SafeLimit))
		row("想探更高可加 --max-rps 再跑一次。")
	default:
		row(fmt.Sprintf("你的服務每秒約能穩定處理 %d 個請求。", result.SafeLimit))
		row(fmt.Sprintf("超過後回應時間會拉長到 %s 以上。", tolerance))
	}

	row("")
	row("這個數字怎麼信？想驗證工具本身量得準不準，")
	row("跑一行 ramplio verify 即可一鍵自證。")

	fmt.Fprintln(w, bot)
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/spf13/cobra"
)

// targetKind is what the single positional argument of `ramplio <arg>` looks like.
type targetKind int

const (
	targetUnknown targetKind = iota
	targetURL
	targetCurl
	targetStdin
	targetYAMLFile
	targetHARFile
)

// discoverTuning holds the probe knobs the smart entry uses. Production uses
// the discover defaults; tests shrink them to keep a run to about a second.
type discoverTuning struct {
	Tolerance     time.Duration
	MaxRPS        int
	ProbeDuration time.Duration
}

// smartEntryEnv carries the I/O and tuning for runSmartEntry.
type smartEntryEnv struct {
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	Tuning discoverTuning
}

func defaultSmartEntryEnv(cmd *cobra.Command) smartEntryEnv {
	return smartEntryEnv{
		In:  cmd.InOrStdin(),
		Out: cmd.OutOrStdout(),
		Err: cmd.ErrOrStderr(),
		Tuning: discoverTuning{
			Tolerance:     defaultDiscoverTolerance,
			MaxRPS:        defaultDiscoverMaxRPS,
			ProbeDuration: defaultDiscoverProbeDuration,
		},
	}
}

// classifyTarget decides what the user handed us. It never touches the network
// or the filesystem: a typo must not start traffic.
func classifyTarget(arg string) targetKind {
	trimmed := strings.TrimSpace(arg)
	lower := strings.ToLower(trimmed)
	switch {
	case trimmed == "-":
		return targetStdin
	case lower == "curl" || strings.HasPrefix(lower, "curl "):
		return targetCurl
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return targetURL
	}
	switch strings.ToLower(filepath.Ext(trimmed)) {
	case ".yaml", ".yml":
		return targetYAMLFile
	case ".har":
		return targetHARFile
	}
	return targetUnknown
}

// runSmartEntry implements `ramplio <網址 | 'curl …' | - | 檔案>`.
func runSmartEntry(ctx context.Context, root *cobra.Command, args []string, env smartEntryEnv) error {
	target, err := joinTargetArgs(args)
	if err != nil {
		return err
	}

	switch classifyTarget(target) {
	case targetURL:
		return smartDiscover(ctx, env, protocols.Request{Method: "GET", URL: target}, nil, false)
	case targetCurl:
		return smartCurl(ctx, env, target)
	case targetStdin:
		return smartStdin(ctx, env)
	case targetYAMLFile:
		printNextSteps(env.Out, "  這是情境檔，請用：", nextStep{"執行情境", "ramplio run --scenario " + target})
		fmt.Fprintln(env.Out)
		return nil
	case targetHARFile:
		printNextSteps(env.Out, "  這是瀏覽器錄製檔（HAR），先轉成情境檔：", nextStep{"轉換", "ramplio import " + target})
		fmt.Fprintln(env.Out)
		return nil
	}
	return unknownTarget(root, env.Out, target)
}

// joinTargetArgs accepts exactly one target. An unquoted `ramplio curl URL`
// arrives as several words; those are rejoined so it still parses.
func joinTargetArgs(args []string) (string, error) {
	if len(args) > 1 && strings.EqualFold(args[0], "curl") {
		return strings.Join(args, " "), nil
	}
	if len(args) != 1 {
		return "", fmt.Errorf("一次只能給一個目標（收到 %d 個）；curl 指令請整段用單引號包起來", len(args))
	}
	return args[0], nil
}

func smartCurl(ctx context.Context, env smartEntryEnv, curl string) error {
	parsed, err := readCurlArg(env.In, curl)
	if err != nil {
		return err
	}
	req := protocols.Request{Method: parsed.Method, URL: parsed.URL, Headers: parsed.HeadersCopy(), Body: parsed.Body}
	return smartDiscover(ctx, env, req, parsed.Warnings, true)
}

// smartStdin handles `pbpaste | ramplio -`: the input may be a curl or a URL.
func smartStdin(ctx context.Context, env smartEntryEnv) error {
	b, err := io.ReadAll(io.LimitReader(env.In, maxCurlInputBytes))
	if err != nil {
		return fmt.Errorf("從 stdin 讀取失敗：%w", err)
	}
	text := strings.TrimSpace(string(b))
	switch classifyTarget(text) {
	case targetCurl:
		return smartCurl(ctx, env, text)
	case targetURL:
		return smartDiscover(ctx, env, protocols.Request{Method: "GET", URL: text}, nil, false)
	}
	if text == "" {
		return errors.New("stdin 是空的：請先在瀏覽器 DevTools 對請求按「Copy as cURL」再執行 pbpaste | ramplio -")
	}
	return errors.New("stdin 的內容不是 curl 指令也不是網址（應以 curl 或 http(s):// 開頭）")
}

// smartDiscover runs the shared discover path. A write method stops after one
// preflight; that is guidance, not failure, so the smart entry exits 0.
func smartDiscover(ctx context.Context, env smartEntryEnv, req protocols.Request, warnings []string, fromCurl bool) error {
	printCurlWarnings(env.Out, warnings)
	err := runDiscover(ctx, env.Out, env.Err, discoverOptions{
		Request:       req,
		Tolerance:     env.Tuning.Tolerance,
		MaxRPS:        env.Tuning.MaxRPS,
		ProbeDuration: env.Tuning.ProbeDuration,
		FromCurl:      fromCurl,
	})
	if errors.Is(err, errWriteNotAllowed) {
		return nil
	}
	return err
}

// unknownTarget refuses to run and points the user somewhere useful: a
// close-matching subcommand, a missing scheme, and the welcome screen.
func unknownTarget(root *cobra.Command, w io.Writer, target string) error {
	msg := fmt.Sprintf("看不懂「%s」：它不是網址（http:// 或 https:// 開頭）、curl 指令，也不是情境檔", target)
	if suggestions := root.SuggestionsFor(target); len(suggestions) > 0 {
		msg += fmt.Sprintf("。你是不是要 %s？", strings.Join(suggestions, " 或 "))
	} else if looksLikeHost(target) {
		msg += fmt.Sprintf("。要測這個網址的話請加上 https://：ramplio https://%s", target)
	}
	printWelcome(w)
	return errors.New(msg)
}

// looksLikeHost is a cheap heuristic for "example.com/path" typed without a scheme.
func looksLikeHost(s string) bool {
	host, _, _ := strings.Cut(s, "/")
	return strings.Contains(host, ".") && !strings.ContainsAny(s, " \t")
}

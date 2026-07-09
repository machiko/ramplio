package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "引導式建立 scenario.yaml",
		Long:  "透過問答方式，一步步產生壓力測試的情境檔案，不需要手寫 YAML。",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWizard()
		},
	}
}

type wizardAuth struct {
	kind          string // "cookie" | "jwt" | ""
	csvFile       string
	cookieName    string
	loginPath     string
	emailField    string
	passwordField string
	loginEmail    string
	loginPass     string
	tokenPath     string
}

type wizardStep struct {
	name       string
	path       string
	method     string
	body       string
	statusCode string
	pauseMs    int
}

func runWizard() error {
	sc := bufio.NewScanner(os.Stdin)

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Ramplio Scenario 建立精靈")
	fmt.Println("  回答幾個問題，自動產生 YAML 情境檔案")
	fmt.Println("  （直接按 Enter 使用括號內的預設值）")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	// ── 1. 基本資訊 ──────────────────────────────────────────
	fmt.Println("【基本設定】")
	name := wPrompt(sc, "這次壓測的名稱是什麼？", "API 功能壓力測試")
	baseURL := wRequired(sc, "網站的網址？（例如 https://example.com）")

	// ── 2. 登入設定 ──────────────────────────────────────────
	fmt.Println()
	fmt.Println("【登入設定】")
	needsLogin := wYN(sc, "這個網站需要登入才能進入主要頁面嗎？", false)

	var auth wizardAuth

	if needsLogin {
		fmt.Println()
		fmt.Println("  登入後，伺服器用哪種方式記錄登入狀態？")
		fmt.Println("  [1] Session Cookie（PHP / Laravel / Rails / Django 常見）")
		fmt.Println("  [2] JWT / Access Token（API 服務常見，登入後從 JSON 取得 token）")
		choice := wChoice(sc, "請輸入 1 或 2", []string{"1", "2"}, "1")

		if choice == "1" {
			auth.kind = "cookie"
			fmt.Println()
			fmt.Println("  ── Session Cookie 設定 ──")
			fmt.Println("  你需要先用以下指令預先登入，取得 session cookie：")
			fmt.Println()
			fmt.Println("    BASE_URL=" + baseURL + " \\")
			fmt.Println("    COOKIE_NAME=session \\")
			fmt.Println("    COUNT=200 \\")
			fmt.Println("    ./scripts/generate_sessions.sh")
			fmt.Println()
			auth.csvFile = wPrompt(sc, "sessions.csv 的路徑？", "sessions.csv")
			auth.cookieName = wPrompt(sc, "Cookie 的名稱是什麼？（在 DevTools → Application → Cookies 可以看到）", "session")
		} else {
			auth.kind = "jwt"
			fmt.Println()
			fmt.Println("  ── JWT Token 設定 ──")
			auth.loginPath = wPrompt(sc, "登入 API 的路徑？（例如 /auth/login）", "/auth/login")
			auth.emailField = wPrompt(sc, "帳號欄位名稱？（例如 email、username）", "email")
			auth.passwordField = wPrompt(sc, "密碼欄位名稱？", "password")
			auth.loginEmail = wRequired(sc, "測試用帳號？（例如 loadtest@example.com）")
			auth.loginPass = wRequired(sc, "測試用密碼？")
			auth.tokenPath = wPrompt(sc, "JWT token 在回應 JSON 的哪個欄位？（例如 $.access_token）", "$.access_token")
		}
	}

	// ── 3. 測試步驟 ──────────────────────────────────────────
	fmt.Println()
	fmt.Println("【測試步驟】")
	fmt.Println("  設定要測試的頁面或 API（可加入多個）")

	var steps []wizardStep

	for {
		fmt.Println()
		fmt.Printf("  步驟 %d\n", len(steps)+1)

		path := wRequired(sc, "  頁面 / API 路徑？（例如 /dashboard）")

		fmt.Println("  HTTP 方法？")
		fmt.Println("  [1] GET（瀏覽頁面、讀取資料）")
		fmt.Println("  [2] POST（送出表單、建立資料）")
		fmt.Println("  [3] PUT（修改資料）")
		fmt.Println("  [4] DELETE（刪除資料）")
		methodMap := map[string]string{"1": "GET", "2": "POST", "3": "PUT", "4": "DELETE"}
		method := methodMap[wChoice(sc, "  請輸入 1-4", []string{"1", "2", "3", "4"}, "1")]

		var body string
		if method == "POST" || method == "PUT" {
			body = wPrompt(sc, "  Request body（JSON 格式，直接按 Enter 略過）", "")
		}

		status := wPrompt(sc, "  期望的 HTTP 狀態碼？（200 或 2xx 表示任何成功回應）", "200")

		pauseMs := 0
		pauseStr := wPrompt(sc, "  這個步驟後要暫停多久？（模擬使用者瀏覽行為，例如 500，單位毫秒，直接按 Enter 略過）", "")
		if pauseStr != "" {
			if v, err := strconv.Atoi(strings.TrimSpace(pauseStr)); err == nil {
				pauseMs = v
			}
		}

		steps = append(steps, wizardStep{
			name:       method + " " + path,
			path:       path,
			method:     method,
			body:       body,
			statusCode: status,
			pauseMs:    pauseMs,
		})

		if !wYN(sc, "  還有其他要測試的頁面或 API 嗎？", false) {
			break
		}
	}

	// ── 4. 負載設定 ──────────────────────────────────────────
	fmt.Println()
	fmt.Println("【負載設定】")

	vusStr := wPrompt(sc, "預計同時有幾個用戶？", "50")
	vus, err := strconv.Atoi(strings.TrimSpace(vusStr))
	if err != nil || vus < 1 {
		vus = 50
	}

	durationStr := wPrompt(sc, "測試持續多久？（例如 3m、30s、1m30s）", "3m")

	fmt.Println("  流量模式？")
	fmt.Println("  [1] 平穩持載（日常流量模擬，推薦入門使用）")
	fmt.Println("  [2] 尖峰湧入（模擬突然大量用戶，例如促銷活動開始）")
	fmt.Println("  [3] 長時間耐壓（持續壓力，找出記憶體洩漏等問題）")
	shapeMap := map[string]string{"1": "steady", "2": "spike", "3": "soak"}
	shape := shapeMap[wChoice(sc, "  請輸入 1-3", []string{"1", "2", "3"}, "1")]

	// ── 5. 閾值設定 ──────────────────────────────────────────
	fmt.Println()
	fmt.Println("【通過 / 失敗標準（可選）】")
	fmt.Println("  設定後，壓測結果超標時 ramplio 會以 exit code 1 退出（可整合 CI）")

	errPctStr := wPrompt(sc, "錯誤率超過多少 % 算失敗？（例如 1，直接按 Enter 略過）", "")
	p95Str := wPrompt(sc, "p95 回應時間超過多少毫秒算失敗？（例如 500，直接按 Enter 略過）", "")

	// ── 6. 輸出 ──────────────────────────────────────────────
	fmt.Println()
	outputFile := wPrompt(sc, "要把 scenario 存成哪個檔名？", "scenario.yaml")
	if !strings.HasSuffix(outputFile, ".yaml") && !strings.HasSuffix(outputFile, ".yml") {
		outputFile += ".yaml"
	}

	// ── 7. 產生 YAML ─────────────────────────────────────────
	yaml := generateYAML(name, baseURL, auth, steps, vus, durationStr, shape, errPctStr, p95Str)

	if err := os.WriteFile(outputFile, []byte(yaml), 0644); err != nil {
		return fmt.Errorf("寫入檔案失敗：%w", err)
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("  ✓  已產生 %s\n", outputFile)
	fmt.Println()
	fmt.Println("  驗證格式：")
	fmt.Printf("    ramplio validate --scenario %s\n", outputFile)
	fmt.Println()
	fmt.Println("  執行壓測：")
	fmt.Printf("    ramplio run --scenario %s\n", outputFile)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	return nil
}

func generateYAML(
	name, baseURL string,
	auth wizardAuth,
	steps []wizardStep,
	vus int,
	duration, shape string,
	errPctStr, p95Str string,
) string {
	var b strings.Builder

	b.WriteString("name: " + yq(name) + "\n\n")
	b.WriteString("vars:\n")
	b.WriteString("  base_url: " + yq(baseURL) + "\n")

	if auth.kind == "cookie" {
		b.WriteString("\nvars_from:\n")
		b.WriteString("  file: " + auth.csvFile + "\n")
		b.WriteString("  mode: sequential\n")
	}

	b.WriteString("\n")
	b.WriteString(stagesYAML(vus, duration, shape))

	if auth.kind == "jwt" {
		loginBody := fmt.Sprintf(`{%q: %q, %q: %q}`,
			auth.emailField, auth.loginEmail, auth.passwordField, auth.loginPass)
		b.WriteString("\nsetup:\n")
		b.WriteString("  - name: 登入取得 JWT\n")
		b.WriteString("    method: POST\n")
		b.WriteString("    url: \"{{vars.base_url}}" + auth.loginPath + "\"\n")
		b.WriteString("    headers:\n")
		b.WriteString("      Content-Type: application/json\n")
		b.WriteString("    body: '" + loginBody + "'\n")
		b.WriteString("    assertions:\n")
		b.WriteString("      status: 200\n")
		b.WriteString("    capture:\n")
		b.WriteString("      jwt: \"" + auth.tokenPath + "\"\n")
	}

	b.WriteString("\nsteps:\n")
	for _, s := range steps {
		b.WriteString("  - name: " + yq(s.name) + "\n")
		b.WriteString("    method: " + s.method + "\n")
		b.WriteString("    url: \"{{vars.base_url}}" + s.path + "\"\n")

		switch auth.kind {
		case "cookie":
			b.WriteString("    headers:\n")
			b.WriteString("      Cookie: \"{{data.session_cookie}}\"\n")
			if s.body != "" {
				b.WriteString("      Content-Type: application/json\n")
			}
		case "jwt":
			b.WriteString("    auth:\n")
			b.WriteString("      bearer: \"{{capture.jwt}}\"\n")
			if s.body != "" {
				b.WriteString("    headers:\n")
				b.WriteString("      Content-Type: application/json\n")
			}
		default:
			if s.body != "" {
				b.WriteString("    headers:\n")
				b.WriteString("      Content-Type: application/json\n")
			}
		}

		if s.body != "" {
			b.WriteString("    body: '" + s.body + "'\n")
		}

		b.WriteString("    assertions:\n")
		b.WriteString("      status: " + s.statusCode + "\n")

		if s.pauseMs > 0 {
			fmt.Fprintf(&b, "    pause: %dms\n", s.pauseMs)
		}
	}

	if errPctStr != "" || p95Str != "" {
		b.WriteString("\nthresholds:\n")
		if errPctStr != "" {
			b.WriteString("  error_rate_pct: " + errPctStr + "\n")
		}
		if p95Str != "" {
			b.WriteString("  p95_ms: " + p95Str + "\n")
		}
	}

	return b.String()
}

func stagesYAML(vus int, duration, shape string) string {
	var b strings.Builder
	b.WriteString("stages:\n")
	vuStr := strconv.Itoa(vus)

	switch shape {
	case "spike":
		b.WriteString("  - duration: 10s\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 30s\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 20s\n    target: 0\n")
	case "soak":
		b.WriteString("  - duration: 1m\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: " + duration + "\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 30s\n    target: 0\n")
	default: // steady
		b.WriteString("  - duration: 30s\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: " + duration + "\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 30s\n    target: 0\n")
	}
	return b.String()
}

// yq wraps a YAML string value in quotes when it contains special characters.
func yq(s string) string {
	if strings.ContainsAny(s, `:#{}&*!|>'"%@`) || strings.Contains(s, "  ") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// ── 輸入輔助函數 ────────────────────────────────────────────

func wPrompt(sc *bufio.Scanner, question, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("  %s（預設：%s）\n  > ", question, defaultVal)
	} else {
		fmt.Printf("  %s\n  > ", question)
	}
	if sc.Scan() {
		if v := strings.TrimSpace(sc.Text()); v != "" {
			return v
		}
	}
	return defaultVal
}

func wRequired(sc *bufio.Scanner, question string) string {
	for {
		fmt.Printf("  %s\n  > ", question)
		if sc.Scan() {
			if v := strings.TrimSpace(sc.Text()); v != "" {
				return v
			}
		}
		fmt.Println("  ⚠  這個欄位必填，請重新輸入。")
	}
}

func wYN(sc *bufio.Scanner, question string, defaultYes bool) bool {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	fmt.Printf("  %s [%s]\n  > ", question, hint)
	if sc.Scan() {
		switch strings.ToLower(strings.TrimSpace(sc.Text())) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
	return defaultYes
}

func wChoice(sc *bufio.Scanner, question string, valid []string, defaultVal string) string {
	for {
		fmt.Printf("  %s（預設：%s）\n  > ", question, defaultVal)
		if sc.Scan() {
			v := strings.TrimSpace(sc.Text())
			if v == "" {
				return defaultVal
			}
			for _, opt := range valid {
				if v == opt {
					return v
				}
			}
		}
		fmt.Printf("  ⚠  請輸入 %s 其中一個選項。\n", strings.Join(valid, " / "))
	}
}

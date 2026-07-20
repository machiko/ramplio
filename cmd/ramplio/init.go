package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/machiko/ramplio/v3/internal/scenariogen"
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

	var auth scenariogen.Auth

	if needsLogin {
		fmt.Println()
		fmt.Println("  登入後，伺服器用哪種方式記錄登入狀態？")
		fmt.Println("  [1] Session Cookie（PHP / Laravel / Rails / Django 常見）")
		fmt.Println("  [2] JWT / Access Token（API 服務常見，登入後從 JSON 取得 token）")
		choice := wChoice(sc, "請輸入 1 或 2", []string{"1", "2"}, "1")

		if choice == "1" {
			auth.Kind = "cookie"
			fmt.Println()
			fmt.Println("  ── Session Cookie 設定 ──")
			fmt.Println("  你需要先用以下指令預先登入，取得 session cookie：")
			fmt.Println()
			fmt.Println("    BASE_URL=" + baseURL + " \\")
			fmt.Println("    COOKIE_NAME=session \\")
			fmt.Println("    COUNT=200 \\")
			fmt.Println("    ./scripts/generate_sessions.sh")
			fmt.Println()
			auth.CSVFile = wPrompt(sc, "sessions.csv 的路徑？", "sessions.csv")
			auth.CookieName = wPrompt(sc, "Cookie 的名稱是什麼？（在 DevTools → Application → Cookies 可以看到）", "session")
		} else {
			auth.Kind = "jwt"
			fmt.Println()
			fmt.Println("  ── JWT Token 設定 ──")
			auth.LoginPath = wPrompt(sc, "登入 API 的路徑？（例如 /auth/login）", "/auth/login")
			auth.EmailField = wPrompt(sc, "帳號欄位名稱？（例如 email、username）", "email")
			auth.PasswordField = wPrompt(sc, "密碼欄位名稱？", "password")
			auth.LoginEmail = wRequired(sc, "測試用帳號？（例如 loadtest@example.com）")
			auth.LoginPass = wRequired(sc, "測試用密碼？")
			auth.TokenPath = wPrompt(sc, "JWT token 在回應 JSON 的哪個欄位？（例如 $.access_token）", "$.access_token")
		}
	}

	// ── 3. 變動參數 / 資料檔 ────────────────────────────────
	// Collected before the test steps so the user knows which {{data.X}} fields
	// exist while writing paths and bodies (a data reference the user cannot
	// foresee is a reference they will not write).
	dataCols, dataFileName, dataRows := promptDataFileConfig(sc, auth)

	// ── 4. 測試步驟 ──────────────────────────────────────────
	fmt.Println()
	fmt.Println("【測試步驟】")
	fmt.Println("  設定要測試的頁面或 API（可加入多個）")

	var steps []scenariogen.Step

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

		steps = append(steps, scenariogen.Step{
			Name:       method + " " + path,
			Path:       path,
			Method:     method,
			Body:       body,
			StatusCode: status,
			PauseMs:    pauseMs,
		})

		if !wYN(sc, "  還有其他要測試的頁面或 API 嗎？", false) {
			break
		}
	}

	// 交叉檢查：步驟引用的 {{data.X}} 與宣告的變動參數欄位是否對得起來，
	// 讓打錯字或宣告了卻沒引用的情況在產生時就浮現，而非拖到 run 才失敗。
	for _, w := range scenariogen.DataParamWarnings(steps, dataCols) {
		fmt.Println("  ⚠  " + w)
	}

	// ── 5. 負載設定 ──────────────────────────────────────────
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

	// ── 6. 閾值設定 ──────────────────────────────────────────
	fmt.Println()
	fmt.Println("【通過 / 失敗標準（可選）】")
	fmt.Println("  設定後，壓測結果超標時 ramplio 會以 exit code 1 退出（可整合 CI）")

	errPctStr := wPrompt(sc, "錯誤率超過多少 % 算失敗？（例如 1，直接按 Enter 略過）", "")
	p95Str := wPrompt(sc, "p95 回應時間超過多少毫秒算失敗？（例如 500，直接按 Enter 略過）", "")

	// ── 7. 輸出 ──────────────────────────────────────────────
	fmt.Println()
	outputFile := wPrompt(sc, "要把 scenario 存成哪個檔名？", "scenario.yaml")
	lowerOut := strings.ToLower(outputFile)
	if !strings.HasSuffix(lowerOut, ".yaml") && !strings.HasSuffix(lowerOut, ".yml") {
		outputFile += ".yaml"
	}

	// ── 8. 產生 YAML ─────────────────────────────────────────
	yaml := scenariogen.GenerateYAML(name, baseURL, auth, steps, vus, durationStr, shape, errPctStr, p95Str, dataFileName)

	if err := os.WriteFile(outputFile, []byte(yaml), 0644); err != nil {
		return fmt.Errorf("寫入檔案失敗：%w", err)
	}

	// 產生配套的資料檔（若有定義變動參數）
	if len(dataCols) > 0 && dataFileName != "" {
		csvContent, err := scenariogen.GenerateCSV(dataCols, dataRows)
		if err != nil {
			return fmt.Errorf("產生資料檔失敗：%w", err)
		}
		if err := os.WriteFile(dataFileName, []byte(csvContent), 0644); err != nil {
			return fmt.Errorf("寫入資料檔失敗：%w", err)
		}
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

// maxDataRows caps generated data-file rows so a mistyped count (e.g. an extra
// zero) cannot try to materialize an unreasonably large file.
const maxDataRows = 1_000_000

// defaultDataRows is the row count used when the user accepts the default or
// enters an unparseable / non-positive value.
const defaultDataRows = 100

// promptDataFileConfig gathers optional data-driven parameters and returns the
// columns, the output CSV filename, and the row count. Empty results mean the
// user opted out, or that cookie auth already owns vars_from (which a data file
// would otherwise conflict with — a scenario has a single vars_from source).
func promptDataFileConfig(sc *bufio.Scanner, auth scenariogen.Auth) ([]scenariogen.DataColumn, string, int) {
	fmt.Println()
	fmt.Println("【變動參數 / 資料檔（可選）】")
	if auth.Kind == "cookie" {
		fmt.Println("  ⓘ  目前使用登入 session 資料檔，此版本尚不支援再疊加變動參數，略過。")
		return nil, "", 0
	}

	fmt.Println("  讓每個虛擬用戶帶不同的參數值（例如不同 user_id、搜尋關鍵字）。")
	fmt.Println("  接下來設定測試步驟時，在路徑或 body 中用 {{data.欄位名}} 引用，ramplio 會自動生成資料檔。")
	if !wYN(sc, "要設定變動參數嗎？", false) {
		return nil, "", 0
	}

	// collectDataColumns always returns at least one column (the first field name
	// is required), so no empty-result guard is needed here.
	cols := collectDataColumns(sc)

	rows := defaultDataRows
	rowsStr := wPrompt(sc, "要產生幾列資料？（建議 >= 虛擬用戶數）", strconv.Itoa(defaultDataRows))
	if v, err := strconv.Atoi(strings.TrimSpace(rowsStr)); err == nil && v > 0 {
		rows = v
	}
	if rows > maxDataRows {
		fmt.Printf("  ⚠  列數上限為 %d，已自動調整。\n", maxDataRows)
		rows = maxDataRows
	}

	fileName := wPrompt(sc, "資料檔要存成哪個檔名？", "data.csv")
	if !strings.HasSuffix(strings.ToLower(fileName), ".csv") {
		fileName += ".csv"
	}
	return cols, fileName, rows
}

// collectDataColumns interactively gathers the variable-parameter columns the
// user wants in the generated data file. Value generation itself lives in
// scenariogen.GenerateCSV; this only maps answers to DataColumn declarations.
func collectDataColumns(sc *bufio.Scanner) []scenariogen.DataColumn {
	var cols []scenariogen.DataColumn
	for {
		name := wRequired(sc, "  參數欄位名稱？（例如 user_id、keyword）")
		if dataColumnNamed(cols, name) {
			fmt.Printf("  ⚠  已經有一個叫 %q 的欄位了，請換一個名稱。\n", name)
			continue
		}

		fmt.Println("  這個欄位的值怎麼產生？")
		fmt.Println("  [1] 遞增數字（1, 2, 3…，適合 ID）")
		fmt.Println("  [2] 隨機 UUID")
		fmt.Println("  [3] Email（loadtest+1@example.com…）")
		fmt.Println("  [4] 自訂清單（從你提供的值中循環挑選）")
		fmt.Println("  [5] 留空範本（產生 <欄名> 佔位，之後自己填真實資料）")
		choice := wChoice(sc, "  請輸入 1-5", []string{"1", "2", "3", "4", "5"}, "1")

		col := scenariogen.DataColumn{Name: name}
		switch choice {
		case "1":
			col.Kind = scenariogen.KindIntSeq
			col.StartSet = true
			startStr := wPrompt(sc, "  從哪個數字開始？", "1")
			if v, err := strconv.Atoi(strings.TrimSpace(startStr)); err == nil {
				col.Start = v
			} else {
				col.Start = 1
			}
		case "2":
			col.Kind = scenariogen.KindUUID
		case "3":
			col.Kind = scenariogen.KindEmail
		case "4":
			col.Kind = scenariogen.KindList
			for {
				raw := wRequired(sc, "  請輸入清單值，用逗號分隔（例如 apple,banana,cherry）")
				for _, v := range strings.Split(raw, ",") {
					if t := strings.TrimSpace(v); t != "" {
						col.ListValues = append(col.ListValues, t)
					}
				}
				if len(col.ListValues) > 0 {
					break
				}
				fmt.Println("  ⚠  至少要提供一個值。")
			}
		case "5":
			col.Kind = scenariogen.KindPlaceholder
		}
		cols = append(cols, col)

		if !wYN(sc, "  還有其他參數欄位嗎？", false) {
			break
		}
	}
	return cols
}

// dataColumnNamed reports whether cols already contains a column called name.
func dataColumnNamed(cols []scenariogen.DataColumn, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return true
		}
	}
	return false
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

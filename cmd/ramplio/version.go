package main

import "runtime/debug"

// resolveVersion 決定 --version 顯示的版本號,優先序:
// 1. ldflags 注入(Makefile / GoReleaser 建置)
// 2. module build info(go install 建置時 Go 會嵌入 module 版本)
// 3. 預設 dev(本地 go build,無版本資訊可用)
//
// 設計成純函式以便測試;build info 由呼叫端讀取後傳入。
func resolveVersion(injected string, bi *debug.BuildInfo) string {
	if injected != "dev" {
		return injected
	}
	if bi == nil || bi.Main.Version == "" || bi.Main.Version == "(devel)" {
		return injected
	}
	// 工作樹有未提交變更時,VCS stamping 給的是「最近的舊 tag」,
	// 顯示它會被誤認為正式版——不採信,維持 dev。
	for _, s := range bi.Settings {
		if s.Key == "vcs.modified" && s.Value == "true" {
			return injected
		}
	}
	return bi.Main.Version
}

// buildVersion 是 main 使用的實際進入點:讀取執行檔內嵌的 build info。
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return resolveVersion(version, nil)
	}
	return resolveVersion(version, bi)
}

package main

import "testing"

// 版本號必須來自可被 ldflags 注入的 package 變數(-X main.version=...),
// 否則 GoReleaser 跨平台建置時無法蓋掉寫死的字串。
func TestRootCmdVersionWiredToInjectableVar(t *testing.T) {
	if version == "" {
		t.Fatal("version 變數不可為空字串,未注入時應有預設值")
	}
	if rootCmd.Version != buildVersion() {
		t.Fatalf("rootCmd.Version = %q,應等於 buildVersion() %q(ldflags 優先、build info 回退)", rootCmd.Version, buildVersion())
	}
}

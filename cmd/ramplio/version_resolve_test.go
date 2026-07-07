package main

import (
	"runtime/debug"
	"testing"
)

// go install 建置不經過 ldflags,版本必須能從 module build info 回退取得;
// ldflags 注入(正式發布路徑)永遠優先。
func TestResolveVersion(t *testing.T) {
	biWith := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: v}}
	}
	biDirty := func(v string) *debug.BuildInfo {
		bi := biWith(v)
		bi.Settings = []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}
		return bi
	}

	tests := []struct {
		name     string
		injected string
		bi       *debug.BuildInfo
		want     string
	}{
		{"ldflags 注入時優先於 build info", "v9.9.9", biWith("v2.1.2"), "v9.9.9"},
		{"未注入時回退到 module 版本(go install 情境)", "dev", biWith("v2.1.2"), "v2.1.2"},
		{"本地 go build 的 (devel) 不當成版本", "dev", biWith("(devel)"), "dev"},
		{"build info 版本為空時保持 dev", "dev", biWith(""), "dev"},
		{"無 build info 時保持 dev", "dev", nil, "dev"},
		// 工作樹有未提交變更時,VCS stamping 的版本是「最近的舊 tag」,
		// 顯示它會讓使用者誤以為在跑正式版——不採信,回退 dev。
		{"vcs.modified 為 true 時不採信版本", "dev", biDirty("v2.1.2"), "dev"},
		// pseudo-version 含 commit 時間戳與 hash,可定位到確切程式碼,
		// 屬可接受的降級顯示(go install 無 tag repo 的情境)。
		{"pseudo-version 屬可接受的降級顯示", "dev", biWith("v0.0.0-20260707120000-abcdef123456"), "v0.0.0-20260707120000-abcdef123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.injected, tt.bi); got != tt.want {
				t.Fatalf("resolveVersion(%q, %+v) = %q, want %q", tt.injected, tt.bi, got, tt.want)
			}
		})
	}
}

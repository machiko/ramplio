package main

// dashController 的基準比較面:GUI 上傳 baseline 的保存與查詢;
// 比較本身發生在 runLoop 組裝結果時(run 生命週期,見 dashcontrol.go)。

import (
	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/dashboard"
)

// LoadBaseline 解析上傳的基準並保存供 run 結束後比較。
// 壞資料大聲失敗,且不覆蓋既有的合法基準。
func (c *dashController) LoadBaseline(raw []byte) (dashboard.BaselineInfo, error) {
	b, err := baseline.Parse(raw)
	if err != nil {
		return dashboard.BaselineInfo{}, err
	}
	c.mu.Lock()
	c.pendingBaseline = &b
	c.mu.Unlock()
	return dashboard.BaselineInfoFrom(b), nil
}

// ClearBaseline 移除已載入的基準,之後的 run 不再比較。
func (c *dashController) ClearBaseline() {
	c.mu.Lock()
	c.pendingBaseline = nil
	c.mu.Unlock()
}

// BaselineMeta 回傳已載入基準的摘要;nil 表示未載入。
func (c *dashController) BaselineMeta() *dashboard.BaselineInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pendingBaseline == nil {
		return nil
	}
	info := dashboard.BaselineInfoFrom(*c.pendingBaseline)
	return &info
}

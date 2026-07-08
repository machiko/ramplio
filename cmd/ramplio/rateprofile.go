package main

import (
	"fmt"
	"time"
)

// rateProfile 計算 --rps 模式的三階段時長(¼ 爬升 + ½ 持平 + ¼ 收尾,
// 爬升至少 1s;duration 過短時持平段為 0)。runRPS 與 --observe 窗口共用,
// 兩處的窗口數學不可分歧。
func rateProfile(dur time.Duration) (rampDur, holdDur time.Duration) {
	rampDur = dur / 4
	if rampDur < time.Second {
		rampDur = time.Second
	}
	holdDur = dur - 2*rampDur
	if holdDur < 0 {
		holdDur = 0
	}
	return rampDur, holdDur
}

// rateProfileLine 描述 --rps 模式的三階段負載輪廓。
// 平均 RPS 含爬升/收尾段、必然低於目標速率,不揭露輪廓的話,
// 使用者會把「平均 < 目標」誤讀成工具打不出目標速率。
func rateProfileLine(targetRPS int, rampDur, holdDur time.Duration) string {
	// duration 過短時(≤ 2×rampDur)沒有持平段;顯示負/零時長比不揭露更誤導。
	if holdDur <= 0 {
		return fmt.Sprintf(
			"負載輪廓:爬升 %s(0→%d)→ 收尾 %s(%d→0);duration 過短、無持平段;報告的「每秒請求」為含爬升段的平均值",
			rampDur, targetRPS, rampDur, targetRPS)
	}
	return fmt.Sprintf(
		"負載輪廓:爬升 %s(0→%d)→ 持平 %s(%d req/s)→ 收尾 %s(%d→0);報告的「每秒請求」為含爬升段的平均值",
		rampDur, targetRPS, holdDur, targetRPS, rampDur, targetRPS)
}

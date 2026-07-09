package main

import (
	"strings"
	"testing"
	"time"
)

// --rps 模式含 ramp-up/ramp-down 階段,平均 RPS 必然低於目標速率;
// 啟動訊息必須揭露負載輪廓,否則使用者會把「平均 150 < 目標 200」誤讀成工具失準。
// 極短 duration 下 holdDur 會歸零甚至為負,輪廓行絕不可顯示負時長。
func TestRateProfileLineDisclosesRampStages(t *testing.T) {
	tests := []struct {
		name       string
		holdDur    time.Duration
		wantSubstr []string
		banSubstr  []string
	}{
		{
			name:       "正常時長顯示三階段",
			holdDur:    5 * time.Second,
			wantSubstr: []string{"2.5s", "5s", "200", "平均"},
		},
		{
			name:       "holdDur 為零時標示無持平段",
			holdDur:    0,
			wantSubstr: []string{"無持平段", "平均"},
			banSubstr:  []string{"持平 0s"},
		},
		{
			name:       "holdDur 為負時不得顯示負時長",
			holdDur:    -1 * time.Second,
			wantSubstr: []string{"無持平段"},
			banSubstr:  []string{"-1s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := rateProfileLine(200, 2500*time.Millisecond, tt.holdDur)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(line, want) {
					t.Fatalf("負載輪廓應含 %q,實際: %s", want, line)
				}
			}
			for _, ban := range tt.banSubstr {
				if strings.Contains(line, ban) {
					t.Fatalf("負載輪廓不得含 %q,實際: %s", ban, line)
				}
			}
		})
	}
}

// rateStages 是 rate 模式三階段的唯一組裝點(CLI runRPS 與 dashboard 共用),
// 兩處窗口數學曾經分歧:dashboard 路徑缺負值鉗制,短 duration 會把
// 負時長 stage 送進 engine。此測試釘死單一來源的正確性。
func TestRateStagesNeverNegative(t *testing.T) {
	for _, dur := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 10 * time.Second} {
		stages := rateStages(100, dur)
		if len(stages) != 3 {
			t.Fatalf("dur=%v 應為三階段,得到 %d", dur, len(stages))
		}
		for i, s := range stages {
			if s.Duration < 0 {
				t.Fatalf("dur=%v stage[%d] 時長為負: %v", dur, i, s.Duration)
			}
		}
		if stages[0].TargetRPS != 100 || stages[1].TargetRPS != 100 || stages[2].TargetRPS != 0 {
			t.Fatalf("dur=%v 三階段 RPS 輪廓錯誤: %+v", dur, stages)
		}
	}
}

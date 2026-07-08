package protocols

import (
	"math/rand/v2"
)

const hexDigits = "0123456789abcdef"

// putHex64 把 v 以 16 位小寫 hex 寫進 dst(dst 長度必須 ≥16)。
// 手工編碼取代 fmt.Sprintf:hot path 上 Sprintf 的配置成本在極限吞吐下
// 會造成可量測的退化(A/B 實測約 -7%)。
func putHex64(dst []byte, v uint64) {
	for i := 15; i >= 0; i-- {
		dst[i] = hexDigits[v&0xf]
		v >>= 4
	}
}

// newTraceparent 產生 W3C Trace Context header 值(00-<traceID>-<spanID>-01),
// 讓被測系統的 APM 能把壓測流量與自身 trace 關聯。
// 走壓測 hot path:math/rand/v2 免鎖、固定緩衝手工編碼、單次配置。
// 規範要求 traceID 與 spanID 不可全零,重擲直到非零。
func newTraceparent() string {
	hi, lo := rand.Uint64(), rand.Uint64()
	for hi == 0 && lo == 0 {
		hi, lo = rand.Uint64(), rand.Uint64()
	}
	span := rand.Uint64()
	for span == 0 {
		span = rand.Uint64()
	}

	var b [55]byte // len("00-") + 32 + len("-") + 16 + len("-01")
	b[0], b[1], b[2] = '0', '0', '-'
	putHex64(b[3:19], hi)
	putHex64(b[19:35], lo)
	b[35] = '-'
	putHex64(b[36:52], span)
	b[52], b[53], b[54] = '-', '0', '1'
	return string(b[:])
}

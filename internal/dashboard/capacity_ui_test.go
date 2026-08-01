package dashboard_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The live and result throughput KPIs must render the server-computed
// verdict.capacity (single source shared with the CLI, rpm-1/rpm-2), not a
// frontend-private formatter — otherwise the slow-endpoint RPM conversion is
// missing in the GUI and x.x5 rounding can drift from the terminal.
func TestServer_ServesCapacitySingleSourceUI(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Get(fmt.Sprintf("http://%s/", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	for _, marker := range []string{
		"snap.verdict?.capacity?.value",         // live KPI consumes the shared value
		"finalResult?.verdict?.capacity?.value", // result KPI consumes the shared value
		"snap.verdict?.capacity?.rpm",           // live RPM annotation for sub-1 rps
		"finalResult?.verdict?.capacity?.rpm",   // result RPM annotation for sub-1 rps
	} {
		assert.Contains(t, html, marker, "served HTML missing capacity single-source marker %q", marker)
	}

	// 每分鐘 wording must match the CLI 承受能力卡片 (rpm-1) so both surfaces
	// speak the same sentence for the same number.
	assert.True(t, strings.Contains(html, "每分鐘"), "RPM annotation should use the CLI 每分鐘 wording")
}

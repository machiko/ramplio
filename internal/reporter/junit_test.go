package reporter_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/internal/metrics"
	"github.com/machiko/ramplio/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeSum(wallSec float64) metrics.Summary {
	return metrics.Summary{
		Total:    1000,
		Errors:   0,
		WallTime: time.Duration(wallSec * float64(time.Second)),
	}
}

func TestWriteJUnit_Pass(t *testing.T) {
	var buf bytes.Buffer
	sum := makeSum(62.3)
	require.NoError(t, reporter.WriteJUnit(&buf, sum, "auth-flow", ""))

	out := buf.String()
	assert.Contains(t, out, `<?xml`)
	assert.Contains(t, out, `<testsuites>`)
	assert.Contains(t, out, `failures="0"`)
	assert.Contains(t, out, `name="auth-flow"`)
	assert.NotContains(t, out, `<failure`)
}

func TestWriteJUnit_Fail(t *testing.T) {
	var buf bytes.Buffer
	sum := makeSum(30.0)
	require.NoError(t, reporter.WriteJUnit(&buf, sum, "smoke", "p99 1250ms > 1000ms"))

	out := buf.String()
	assert.Contains(t, out, `failures="1"`)
	assert.Contains(t, out, `<failure`)
	assert.Contains(t, out, `ThresholdViolation`)
	// XML encodes > as &gt; in attribute values.
	assert.Contains(t, out, `p99 1250ms`)
}

func TestWriteJUnit_ValidXMLHeader(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, reporter.WriteJUnit(&buf, makeSum(10), "test", ""))
	assert.True(t, strings.HasPrefix(buf.String(), "<?xml version="))
}

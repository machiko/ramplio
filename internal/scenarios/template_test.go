package scenarios

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderString_UUID(t *testing.T) {
	out, err := RenderString("id-{{uuid}}", nil)
	require.NoError(t, err)
	// UUID v4 is 36 chars: "id-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	assert.Regexp(t, regexp.MustCompile(`^id-[0-9a-f-]{36}$`), out)
}

func TestRenderString_Timestamp(t *testing.T) {
	out, err := RenderString("{{timestamp}}", nil)
	require.NoError(t, err)
	ts, err := strconv.ParseInt(out, 10, 64)
	require.NoError(t, err)
	assert.Greater(t, ts, int64(1_700_000_000))
}

func TestRenderString_TimestampMs(t *testing.T) {
	out, err := RenderString("{{timestamp_ms}}", nil)
	require.NoError(t, err)
	ts, err := strconv.ParseInt(out, 10, 64)
	require.NoError(t, err)
	assert.Greater(t, ts, int64(1_700_000_000_000))
}

func TestRenderString_Env(t *testing.T) {
	os.Setenv("TEST_TOKEN", "secret123")
	defer os.Unsetenv("TEST_TOKEN")

	out, err := RenderString(`Bearer {{env "TEST_TOKEN"}}`, nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret123", out)
}

func TestRenderString_Vars(t *testing.T) {
	ctx := &VarContext{Vars: map[string]string{"base_url": "https://example.com"}}
	out, err := RenderString("{{vars.base_url}}/health", ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/health", out)
}

func TestRenderString_Capture(t *testing.T) {
	ctx := &VarContext{Captures: map[string]string{"token": "tok_abc"}}
	out, err := RenderString("Bearer {{capture.token}}", ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok_abc", out)
}

func TestRenderString_MissingVar(t *testing.T) {
	_, err := RenderString("{{vars.missing}}", &VarContext{Vars: map[string]string{}})
	assert.Error(t, err)
}

func TestRenderString_UnknownToken(t *testing.T) {
	_, err := RenderString("{{bogus}}", nil)
	assert.Error(t, err)
}

func TestRenderString_NoTokens(t *testing.T) {
	out, err := RenderString("plain string", nil)
	require.NoError(t, err)
	assert.Equal(t, "plain string", out)
}

func TestRenderString_Data(t *testing.T) {
	ctx := &VarContext{Data: map[string]string{"email": "user@example.com", "password": "secret"}}
	out, err := RenderString("{{data.email}}", ctx)
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", out)
}

func TestRenderString_DataMissing(t *testing.T) {
	ctx := &VarContext{Data: map[string]string{}}
	_, err := RenderString("{{data.missing}}", ctx)
	assert.Error(t, err)
}

func TestRenderString_DataNilContext(t *testing.T) {
	_, err := RenderString("{{data.foo}}", nil)
	assert.Error(t, err)
}

func TestRenderHeaders(t *testing.T) {
	ctx := &VarContext{Captures: map[string]string{"token": "tok_xyz"}}
	headers := map[string]string{
		"Authorization": "Bearer {{capture.token}}",
		"Content-Type":  "application/json",
	}
	out, err := RenderHeaders(headers, ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok_xyz", out["Authorization"])
	assert.Equal(t, "application/json", out["Content-Type"])
	assert.True(t, strings.Contains(out["Authorization"], "tok_xyz"))
}

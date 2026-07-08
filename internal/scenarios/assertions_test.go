package scenarios

import (
	"testing"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func TestEvalAssertions_Status(t *testing.T) {
	result := protocols.Result{StatusCode: 200}
	require.NoError(t, EvalAssertions(&Assertions{Status: &StatusCheck{raw: "200"}}, result))
	assert.Error(t, EvalAssertions(&Assertions{Status: &StatusCheck{raw: "404"}}, result))
}

func TestEvalAssertions_StatusWildcard(t *testing.T) {
	tests := []struct {
		pattern string
		code    int
		want    bool
	}{
		{"2xx", 200, true},
		{"2xx", 201, true},
		{"2xx", 299, true},
		{"2xx", 300, false},
		{"2xx", 404, false},
		{"4xx", 404, true},
		{"4xx", 200, false},
		{"5xx", 500, true},
		{"5xx", 503, true},
		{"5xx", 200, false},
		{"3xx", 301, true},
		{"1xx", 100, true},
		{"200", 200, true},
		{"200", 201, false},
	}
	for _, tc := range tests {
		result := protocols.Result{StatusCode: tc.code}
		err := EvalAssertions(&Assertions{Status: &StatusCheck{raw: tc.pattern}}, result)
		if tc.want {
			require.NoError(t, err, "pattern=%s code=%d", tc.pattern, tc.code)
		} else {
			assert.Error(t, err, "pattern=%s code=%d", tc.pattern, tc.code)
		}
	}
}

func TestEvalAssertions_BodyContains(t *testing.T) {
	result := protocols.Result{Body: []byte(`{"ok":true,"msg":"hello"}`)}
	require.NoError(t, EvalAssertions(&Assertions{BodyContains: ptr("hello")}, result))
	assert.Error(t, EvalAssertions(&Assertions{BodyContains: ptr("goodbye")}, result))
}

func TestEvalAssertions_BodyMatches(t *testing.T) {
	result := protocols.Result{Body: []byte(`{"id":42}`)}
	require.NoError(t, EvalAssertions(&Assertions{BodyMatches: ptr(`"id":\d+`)}, result))
	assert.Error(t, EvalAssertions(&Assertions{BodyMatches: ptr(`"id":"`)}, result))
}

func TestEvalAssertions_BodyJSON(t *testing.T) {
	result := protocols.Result{Body: []byte(`{"data":{"token":"abc123"},"items":[{"name":"foo"}]}`)}

	require.NoError(t, EvalAssertions(&Assertions{
		BodyJSON: map[string]string{
			"$.data.token":    "abc123",
			"$.items[0].name": "foo",
		},
	}, result))

	assert.Error(t, EvalAssertions(&Assertions{
		BodyJSON: map[string]string{"$.data.token": "wrong"},
	}, result))
}

func TestEvalAssertions_HeaderEquals(t *testing.T) {
	result := protocols.Result{
		ResponseHeaders: map[string]string{
			"Content-Type": "application/json",
		},
	}
	require.NoError(t, EvalAssertions(&Assertions{
		HeaderEquals: map[string]string{"content-type": "application/json"},
	}, result))
	assert.Error(t, EvalAssertions(&Assertions{
		HeaderEquals: map[string]string{"content-type": "text/html"},
	}, result))
}

func TestEvalAssertions_Nil(t *testing.T) {
	require.NoError(t, EvalAssertions(nil, protocols.Result{}))
}

package scenarios

import (
	"testing"

	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func TestEvalAssertions_Status(t *testing.T) {
	result := protocols.Result{StatusCode: 200}
	require.NoError(t, EvalAssertions(&Assertions{Status: ptr(200)}, result))
	assert.Error(t, EvalAssertions(&Assertions{Status: ptr(404)}, result))
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
			"$.data.token":      "abc123",
			"$.items[0].name":  "foo",
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

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForceStreamTrueJSON(t *testing.T) {
	out, changed, err := ForceStreamTrueJSON([]byte(`{"model":"gpt-test","stream":false,"messages":[]}`), true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, string(out), `"stream":true`)
	assert.Contains(t, string(out), `"include_usage":true`)

	out, changed, err = ForceStreamTrueJSON([]byte(`{"model":"claude-test","stream":true}`), false)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.NotContains(t, string(out), "stream_options")
}

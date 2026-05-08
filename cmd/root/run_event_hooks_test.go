package root

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOnEventFlags(t *testing.T) {
	hooks, err := parseOnEventFlags([]string{
		"stream_stopped=say done",
		"*=tee /tmp/events.log",
	})
	require.NoError(t, err)
	require.Len(t, hooks, 2)
	assert.Equal(t, "stream_stopped", hooks[0].eventType)
	assert.Equal(t, "say done", hooks[0].command)
	assert.Equal(t, "*", hooks[1].eventType)
	assert.Equal(t, "tee /tmp/events.log", hooks[1].command)
}

func TestParseOnEventFlags_BadFormat(t *testing.T) {
	cases := []string{"no-equals", "=missing-type"}
	for _, s := range cases {
		_, err := parseOnEventFlags([]string{s})
		assert.Error(t, err, "expected error for %q", s)
	}
}

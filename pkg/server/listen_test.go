package server

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListen_FD(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	orig, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer orig.Close()

	file, err := orig.(*net.TCPListener).File()
	require.NoError(t, err)
	defer file.Close()

	ln, err := Listen(t.Context(), fmt.Sprintf("fd://%d", file.Fd()))
	require.NoError(t, err)
	defer ln.Close()

	assert.NotNil(t, ln.Addr())
}

func TestListen_FD_InvalidNumber(t *testing.T) {
	t.Parallel()

	_, err := Listen(t.Context(), "fd://notanumber")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file descriptor")
}

func TestListen_FD_NegativeNumber(t *testing.T) {
	t.Parallel()

	_, err := Listen(t.Context(), "fd://-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be > 2")
}

func TestListen_FD_ReservedDescriptors(t *testing.T) {
	t.Parallel()

	for _, fd := range []string{"0", "1", "2"} {
		_, err := Listen(t.Context(), "fd://"+fd)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be > 2")
	}
}

func TestListen_FD_InvalidDescriptor(t *testing.T) {
	t.Parallel()

	// Use a very high fd number that's unlikely to exist
	_, err := Listen(t.Context(), "fd://999999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file descriptor 999999")
}

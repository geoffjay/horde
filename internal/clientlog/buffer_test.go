package clientlog

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufferWriteSplitsLines(t *testing.T) {
	b := NewBuffer(10)
	n, err := b.Write([]byte("first\nsecond\n"))
	require.NoError(t, err)
	assert.Equal(t, len("first\nsecond\n"), n)
	assert.Equal(t, []string{"first", "second"}, b.Lines())
}

func TestBufferSkipsBlankWrites(t *testing.T) {
	b := NewBuffer(10)
	_, err := b.Write([]byte("\n"))
	require.NoError(t, err)
	assert.Empty(t, b.Lines())
	assert.Equal(t, 0, b.Len())
}

func TestBufferEvictsOldestAtCapacity(t *testing.T) {
	b := NewBuffer(3)
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		_, err := b.Write([]byte(line + "\n"))
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"c", "d", "e"}, b.Lines())
}

func TestNewBufferDefaultsCapacity(t *testing.T) {
	b := NewBuffer(0)
	assert.Equal(t, DefaultCapacity, b.capacity)
}

func TestBufferNotifyFiresOnAppend(t *testing.T) {
	b := NewBuffer(10)
	var calls int
	b.SetNotify(func() { calls++ })
	_, err := b.Write([]byte("line\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, calls)

	// A blank write adds no lines and must not notify.
	_, err = b.Write([]byte("\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestBufferConcurrentWrites(t *testing.T) {
	b := NewBuffer(1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = b.Write([]byte("x\n"))
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 500, b.Len())
}

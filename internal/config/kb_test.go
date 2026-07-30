package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_KBSyncEnabled(t *testing.T) {
	c := &Config{}
	assert.False(t, c.KBSyncEnabled(), "disabled by default")

	c.Knowledgebase.Sync.Enabled = true
	assert.True(t, c.KBSyncEnabled())
}

func TestConfig_ValidateKnowledgebase(t *testing.T) {
	base := func() *Config {
		return &Config{
			Mode:   "master",
			Server: ServerConfig{Port: defaultServerPort},
		}
	}

	t.Run("disabled is no-op", func(t *testing.T) {
		c := base()
		assert.NoError(t, c.Validate())
		assert.False(t, c.KBSyncEnabled())
	})

	t.Run("enabled with defaults is valid", func(t *testing.T) {
		c := base()
		c.Knowledgebase.Sync.Enabled = true
		c.Knowledgebase.Sync.PollInterval = "30s"
		c.Knowledgebase.Sync.DebounceMS = 500
		c.Knowledgebase.Sync.MaxFileSize = 1048576
		assert.NoError(t, c.Validate())
	})

	t.Run("enabled with empty poll_interval is valid (uses default)", func(t *testing.T) {
		c := base()
		c.Knowledgebase.Sync.Enabled = true
		c.Knowledgebase.Sync.PollInterval = ""
		assert.NoError(t, c.Validate())
	})

	t.Run("invalid poll_interval rejected", func(t *testing.T) {
		c := base()
		c.Knowledgebase.Sync.Enabled = true
		c.Knowledgebase.Sync.PollInterval = "not-a-duration"
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "poll_interval")
	})

	t.Run("negative debounce rejected", func(t *testing.T) {
		c := base()
		c.Knowledgebase.Sync.Enabled = true
		c.Knowledgebase.Sync.DebounceMS = -1
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "debounce_ms")
	})

	t.Run("negative max_file_size rejected", func(t *testing.T) {
		c := base()
		c.Knowledgebase.Sync.Enabled = true
		c.Knowledgebase.Sync.MaxFileSize = -1
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_file_size")
	})
}

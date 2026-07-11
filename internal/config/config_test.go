package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixturePath returns an absolute path to a testdata fixture.
func fixturePath(name string) string {
	return filepath.Join("testdata", name)
}

func TestLoadConfigWithDefaults_FixtureFormats(t *testing.T) {
	cases := []struct {
		name string
		fixt string
		ext  string
	}{
		{"YAML", "valid.yaml", "yaml"},
		{"JSON", "valid.json", "json"},
		{"TOML", "valid.toml", "toml"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HORDE_CONFIG", fixturePath(tc.fixt))
			Reset()

			c := &Config{}
			err := LoadConfigWithDefaults("horde", c, defaults)
			require.NoError(t, err)

			assert.Equal(t, "testing", c.Env)
			assert.Equal(t, "slave", c.Mode)
			assert.Equal(t, 13500, c.Server.Port)
			assert.Equal(t, "master:13420", c.Server.Leader)
			assert.Equal(t, "test-node", c.Cluster.NodeID)
			assert.Equal(t, "json", c.Log.Formatter)
			assert.Equal(t, "debug", c.Log.Level)
			assert.Equal(t, "org.horde.Test", c.Service.ID)
		})
	}
}

func TestLoadConfigWithDefaults_AppliesDefaults(t *testing.T) {
	// No config file: defaults still apply.
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	Reset()

	c := &Config{}
	err := LoadConfigWithDefaults("horde", c, defaults)
	require.NoError(t, err)

	assert.Equal(t, "development", c.Env)
	assert.Equal(t, "master", c.Mode)
	assert.Equal(t, 13420, c.Server.Port)
	assert.Equal(t, "text", c.Log.Formatter)
	assert.Equal(t, "info", c.Log.Level)
	assert.Equal(t, "org.horde.Horde", c.Service.ID)
	assert.Equal(t, "static", c.Cluster.DiscoveryMechanism)
}

func TestLoadConfigWithDefaults_EnvOverrides(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("valid.yaml"))
	t.Setenv("HORDE_MODE", "master")
	t.Setenv("HORDE_SERVER_PORT", "14000")
	t.Setenv("HORDE_LOG_LEVEL", "warn")
	Reset()

	c := &Config{}
	err := LoadConfigWithDefaults("horde", c, defaults)
	require.NoError(t, err)

	// Env overrides file values.
	assert.Equal(t, "master", c.Mode)
	assert.Equal(t, 14000, c.Server.Port)
	assert.Equal(t, "warn", c.Log.Level)
	// File value preserved where no env override.
	assert.Equal(t, "testing", c.Env)
}

func TestGet_Singleton(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	Reset()

	c1 := Get()
	c2 := Get()
	assert.Same(t, c1, c2)
	assert.Equal(t, "master", c1.Mode)
}

func TestLoad_Idempotent(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	Reset()

	require.NoError(t, Load())
	require.NoError(t, Load()) // second call is a no-op
	assert.Equal(t, "master", Get().Mode)
}

func TestLoadConfig_RejectsUnsupportedExtension(t *testing.T) {
	// Create a temp file with an unsupported extension.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.ini")
	require.NoError(t, os.WriteFile(path, []byte("x=1"), 0o600))
	t.Setenv("HORDE_CONFIG", path)

	c := &Config{}
	err := LoadConfig("horde", c)
	assert.Error(t, err)
}

func TestConfig_Validate(t *testing.T) {
	valid := func() *Config {
		return &Config{
			Mode:   "master",
			Server: ServerConfig{Port: defaultServerPort},
		}
	}

	t.Run("valid master", func(t *testing.T) {
		assert.NoError(t, valid().Validate())
	})

	t.Run("valid slave without leader", func(t *testing.T) {
		c := valid()
		c.Mode = "slave"
		assert.NoError(t, c.Validate())
	})

	t.Run("unknown mode", func(t *testing.T) {
		c := valid()
		c.Mode = "bogus"
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid mode")
	})

	t.Run("port out of range", func(t *testing.T) {
		c := valid()
		c.Server.Port = 0
		assert.Error(t, c.Validate())

		c.Server.Port = 70000
		assert.Error(t, c.Validate())
	})

	t.Run("negative timeout", func(t *testing.T) {
		c := valid()
		c.Server.ReadTimeout = -1
		assert.Error(t, c.Validate())
	})
}

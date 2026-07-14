package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePaths_Defaults(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	Reset()

	c := &Config{}
	require.NoError(t, LoadConfigWithDefaults("horde", c, defaults))
	c.resolvePaths()

	home, _ := os.UserHomeDir()
	assert.Equal(t, filepath.Join(home, ".config", "horde"), c.Paths.ConfigDir)
	assert.Equal(t, filepath.Join(home, ".local", "share", "horde"), c.Paths.DataDir)
	assert.Equal(t, filepath.Join(home, ".local", "state", "horde"), c.Paths.StateDir)
}

func TestResolvePaths_EnvOverrides(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	t.Setenv("HORDE_PATHS_CONFIG_DIR", "/custom/config")
	t.Setenv("HORDE_PATHS_DATA_DIR", "/custom/data")
	t.Setenv("HORDE_PATHS_STATE_DIR", "/custom/state")
	Reset()

	c := &Config{}
	require.NoError(t, LoadConfigWithDefaults("horde", c, defaults))
	c.resolvePaths()

	assert.Equal(t, "/custom/config", c.Paths.ConfigDir)
	assert.Equal(t, "/custom/data", c.Paths.DataDir)
	assert.Equal(t, "/custom/state", c.Paths.StateDir)
}

func TestResolvePaths_PartialEnvOverride(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	t.Setenv("HORDE_PATHS_DATA_DIR", "/custom/data")
	Reset()

	c := &Config{}
	require.NoError(t, LoadConfigWithDefaults("horde", c, defaults))
	c.resolvePaths()

	home, _ := os.UserHomeDir()
	assert.Equal(t, filepath.Join(home, ".config", "horde"), c.Paths.ConfigDir)
	assert.Equal(t, "/custom/data", c.Paths.DataDir)
	assert.Equal(t, filepath.Join(home, ".local", "state", "horde"), c.Paths.StateDir)
}

func TestEnsureDataDirs_CreatesDirectories(t *testing.T) {
	tmp := t.TempDir()
	c := &Config{
		Paths: DataPaths{
			ConfigDir: filepath.Join(tmp, "config"),
			DataDir:   filepath.Join(tmp, "data"),
			StateDir:  filepath.Join(tmp, "state"),
		},
	}

	require.NoError(t, c.EnsureDataDirs())
	assert.DirExists(t, c.Paths.ConfigDir)
	assert.DirExists(t, c.Paths.DataDir)
	assert.DirExists(t, c.Paths.StateDir)
}

func TestEnsureDataDirs_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	c := &Config{
		Paths: DataPaths{
			DataDir: filepath.Join(tmp, "data"),
		},
	}

	require.NoError(t, c.EnsureDataDirs())
	require.NoError(t, c.EnsureDataDirs())
	assert.DirExists(t, c.Paths.DataDir)
}

func TestProjectConfig_Defaults(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	Reset()

	c := &Config{}
	require.NoError(t, LoadConfigWithDefaults("horde", c, defaults))

	assert.Equal(t, ".", c.Project.WorkspaceDir)
	assert.Equal(t, 0, c.Project.ContextRetention)
}

func TestProjectConfig_EnvOverrides(t *testing.T) {
	t.Setenv("HORDE_CONFIG", fixturePath("empty.yaml"))
	t.Setenv("HORDE_PROJECT_WORKSPACE_DIR", "/tmp/work")
	t.Setenv("HORDE_PROJECT_CONTEXT_RETENTION", "600")
	Reset()

	c := &Config{}
	require.NoError(t, LoadConfigWithDefaults("horde", c, defaults))

	assert.Equal(t, "/tmp/work", c.Project.WorkspaceDir)
	assert.Equal(t, 600, c.Project.ContextRetention)
}

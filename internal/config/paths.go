package config

import (
	"fmt"
	"os"
	"path/filepath"

	homedir "github.com/mitchellh/go-homedir"
)

// resolvePaths fills in any empty DataPaths fields with their XDG default
// values, derived from the user's home directory. When an env var
// (HORDE_CONFIG_DIR, HORDE_DATA_DIR, HORDE_STATE_DIR) or config file value
// is set, viper populates the field and this is a no-op for that field.
func (c *Config) resolvePaths() {
	home, err := homedir.Dir()
	if err != nil {
		// Can't determine home; leave paths as-is. The server will
		// surface errors when it tries to use them.
		return
	}

	if c.Paths.ConfigDir == "" {
		c.Paths.ConfigDir = filepath.Join(home, ".config", "horde")
	}
	if c.Paths.DataDir == "" {
		c.Paths.DataDir = filepath.Join(home, ".local", "share", "horde")
	}
	if c.Paths.StateDir == "" {
		c.Paths.StateDir = filepath.Join(home, ".local", "state", "horde")
	}
}

// dirPerm is the permission for horde data directories.
const dirPerm = 0o755

// EnsureDataDirs creates the config, data, and state directories if they do
// not already exist. It is called at server startup so that persistence and
// logging can write to them.
func (c *Config) EnsureDataDirs() error {
	for _, dir := range []string{c.Paths.ConfigDir, c.Paths.DataDir, c.Paths.StateDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			return fmt.Errorf("create data dir %s: %w", dir, err)
		}
	}
	return nil
}

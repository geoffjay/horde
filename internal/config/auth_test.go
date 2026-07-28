package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_AuthEnabled(t *testing.T) {
	c := &Config{}
	assert.False(t, c.AuthEnabled(), "no users ⇒ auth disabled")

	c.Auth.Users = []UserDef{{ID: "alice", Token: "t"}}
	assert.True(t, c.AuthEnabled())
}

func TestConfig_ValidateAuth(t *testing.T) {
	base := func() *Config {
		return &Config{
			Mode:   "master",
			Server: ServerConfig{Port: defaultServerPort},
		}
	}

	t.Run("disabled when no users", func(t *testing.T) {
		c := base()
		assert.NoError(t, c.Validate())
		assert.False(t, c.AuthEnabled())
	})

	t.Run("valid users", func(t *testing.T) {
		c := base()
		c.Auth.Users = []UserDef{
			{ID: "alice", Token: "tok-a"},
			{ID: "bob", Token: "tok-b", Admin: true, AllowedTools: []string{"read"}},
		}
		assert.NoError(t, c.Validate())
		assert.True(t, c.AuthEnabled())
	})

	t.Run("empty id rejected", func(t *testing.T) {
		c := base()
		c.Auth.Users = []UserDef{{Token: "t"}}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-empty id")
	})

	t.Run("empty token rejected", func(t *testing.T) {
		c := base()
		c.Auth.Users = []UserDef{{ID: "alice"}}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-empty token")
	})

	t.Run("duplicate id rejected", func(t *testing.T) {
		c := base()
		c.Auth.Users = []UserDef{
			{ID: "alice", Token: "a"},
			{ID: "alice", Token: "b"},
		}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate user id")
	})

	t.Run("duplicate token rejected", func(t *testing.T) {
		c := base()
		c.Auth.Users = []UserDef{
			{ID: "alice", Token: "same"},
			{ID: "bob", Token: "same"},
		}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate token")
	})

	t.Run("invalid permissions mode rejected", func(t *testing.T) {
		c := base()
		c.Auth.Users = []UserDef{
			{ID: "alice", Token: "t", Permissions: &PermissionScope{Mode: "bogus"}},
		}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid permissions.mode")
	})

	t.Run("valid permissions modes accepted", func(t *testing.T) {
		for _, mode := range []string{"", "read_only", "read_write"} {
			c := base()
			c.Auth.Users = []UserDef{
				{ID: "alice", Token: "t", Permissions: &PermissionScope{Mode: mode}},
			}
			assert.NoError(t, c.Validate(), "mode %q should be accepted", mode)
		}
	})
}

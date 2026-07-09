package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceConfigEmpty(t *testing.T) {
	c := ServiceConfig{}
	assert.Empty(t, c.ID)
}

func TestServiceConfigWithID(t *testing.T) {
	c := ServiceConfig{ID: "org.horde.Horde"}
	assert.Equal(t, "org.horde.Horde", c.ID)
}

func TestServiceConfigIDFormats(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"simple", "horde"},
		{"dotted", "org.horde.Horde"},
		{"hyphenated", "horde-node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.id, ServiceConfig{ID: tc.id}.ID)
		})
	}
}

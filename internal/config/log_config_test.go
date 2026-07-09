package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogConfigEmpty(t *testing.T) {
	c := LogConfig{}
	assert.Empty(t, c.Formatter)
	assert.Empty(t, c.Level)
}

func TestLogConfigTextFormatter(t *testing.T) {
	c := LogConfig{Formatter: "text", Level: "info"}
	assert.Equal(t, "text", c.Formatter)
	assert.Equal(t, "info", c.Level)
}

func TestLogConfigJSONFormatter(t *testing.T) {
	c := LogConfig{Formatter: "json", Level: "debug"}
	assert.Equal(t, "json", c.Formatter)
	assert.Equal(t, "debug", c.Level)
}

func TestLogConfigLogLevels(t *testing.T) {
	for _, level := range []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"} {
		t.Run(level, func(t *testing.T) {
			c := LogConfig{Formatter: "text", Level: level}
			assert.Equal(t, level, c.Level)
		})
	}
}

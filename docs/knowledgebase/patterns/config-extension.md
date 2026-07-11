---
type: Pattern
title: Config extension pattern
description: Embed generic config pieces and add app-specific sections.
tags: [pattern, config]
timestamp: 2026-07-08T00:00:00Z
---

# Pattern

The generic config loader (`internal/config/config.go`) provides
`LoadConfigWithDefaults` and `LoadConfig`. An application defines a concrete
`Config` struct that embeds the common pieces (`LogConfig`, `ServiceConfig`)
and adds its own sections, then calls `LoadConfigWithDefaults` with a
defaults map.

# Example

```go
type Config struct {
    Env     string        `mapstructure:"env"`
    Mode    string        `mapstructure:"mode"`
    Server  ServerConfig  `mapstructure:"server"`
    Log     LogConfig     `mapstructure:"log"`
    Service ServiceConfig `mapstructure:"service"`
}

var defaults = map[string]any{
    "env":         "development",
    "mode":        "master",
    "server.port": 13420,
    // ...
}

func Get() *Config { ... LoadConfigWithDefaults("horde", c, defaults) ... }
```

# Rationale

Follows the `plantd/identity` pattern: one config singleton, defaults in a
map, env overrides via `HORDE_*`, multiple file formats. See
[configuration](/docs/knowledgebase/concepts/configuration.md).

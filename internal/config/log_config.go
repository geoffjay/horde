package config

// LogConfig is a struct to store log configuration.
//
// Formatter is the log formatter to be used ("text" or "json").
// Level is the log level to be used.
//
// Example:
//
//	formatter: "json"
//	level: "info"
type LogConfig struct {
	Formatter string `mapstructure:"formatter"`
	Level     string `mapstructure:"level"`
}

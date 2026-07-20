package config

// LogConfig is a struct to store log configuration.
//
// Formatter is the log formatter to be used ("text" or "json").
// Level is the log level to be used.
// Output is where logs are written: "stderr" (default), "stdout", or "file".
// File is the path written to when Output is "file".
//
// The TUI is a special case: it always captures logs into an in-memory buffer
// (shown on its logs page) rather than stderr, because it renders to the
// terminal and stray log lines would corrupt the display. When the TUI is run
// with Output "file" it additionally tees the same lines to File.
//
// Example:
//
//	formatter: "json"
//	level: "info"
//	output: "file"
//	file: "/var/log/horde.log"
type LogConfig struct {
	Formatter string `mapstructure:"formatter"`
	Level     string `mapstructure:"level"`
	Output    string `mapstructure:"output"`
	File      string `mapstructure:"file"`
}

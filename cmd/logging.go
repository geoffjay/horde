package cmd

import (
	"io"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/internal/config"
)

// logFilePerm is the permission mode for a log file created via log.output
// "file". It is owner read/write only to satisfy gosec (G302) and avoid
// exposing logs to other users.
const logFilePerm = 0o600

// configureLogging sets the global logrus formatter and level from the app
// config. It does not touch the output destination — callers set that
// separately (serve/agent via setupLogging; the TUI redirects to an in-memory
// buffer in app.Run).
func configureLogging(cfg *config.Config) {
	switch cfg.Log.Formatter {
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{})
	default:
		logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	}

	level, err := logrus.ParseLevel(cfg.Log.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logrus.SetLevel(level)
}

// setupLogging configures the global logrus logger (formatter, level, and
// output) from the app config for the non-TUI commands (serve, agent). The
// TUI does not use this: it captures logs into an in-memory buffer instead.
func setupLogging(cfg *config.Config) {
	configureLogging(cfg)
	logrus.SetOutput(resolveLogOutput(cfg))
}

// resolveLogOutput returns the writer for the configured log.output. Unknown
// values and file-open failures fall back to stderr (with a warning) so a
// misconfiguration never silently loses logs.
func resolveLogOutput(cfg *config.Config) io.Writer {
	switch cfg.Log.Output {
	case "", "stderr":
		return os.Stderr
	case "stdout":
		return os.Stdout
	case "file":
		if f := openLogFile(cfg.Log.File); f != nil {
			return f
		}
		return os.Stderr
	default:
		logrus.WithField("output", cfg.Log.Output).
			Warn("unknown log.output; falling back to stderr")
		return os.Stderr
	}
}

// logFileWriter returns an open log file when log.output is "file" and a path
// is set, or nil otherwise. The TUI uses it to tee its in-memory logs to disk
// without writing to stderr (which would corrupt the display).
func logFileWriter(cfg *config.Config) io.Writer {
	if cfg.Log.Output != "file" {
		return nil
	}
	if f := openLogFile(cfg.Log.File); f != nil {
		return f
	}
	return nil
}

// openLogFile opens (creating/appending) the log file at path. It returns nil
// and logs a warning on an empty path or open error; the file handle is
// intentionally not closed — it lives for the process lifetime.
func openLogFile(path string) *os.File {
	if path == "" {
		logrus.Warn("log.output is \"file\" but log.file is empty; falling back to stderr")
		return nil
	}
	// #nosec G304 -- the path comes from operator-controlled config, not
	// untrusted input (same rationale as the agent command in server.go).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePerm)
	if err != nil {
		logrus.WithError(err).WithField("file", path).
			Warn("could not open log file; falling back to stderr")
		return nil
	}
	return f
}

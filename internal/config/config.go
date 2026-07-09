// Package config provides configuration loading functionality.
//
// This package is adapted from github.com/geoffjay/plantd/core/config. It
// loads configuration from a set of search paths, supports YAML, JSON, and
// TOML formats, and applies environment-variable overrides using the
// HORDE_<NAME>_* prefix.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
)

// prepare constructs a viper instance configured to load a config file named
// `name` from the standard search paths, or from an explicit path supplied
// via the HORDE_<NAME>_CONFIG environment variable.
func prepare(name string) (*viper.Viper, error) {
	home, err := homedir.Dir()
	if err != nil {
		return nil, err
	}

	envPrefix := fmt.Sprintf("HORDE_%s", strings.ToUpper(name))
	envConfig := fmt.Sprintf("%s_CONFIG", envPrefix)

	v := viper.New()

	file := os.Getenv(envConfig)
	if file == "" {
		v.SetConfigName(name)
		v.AddConfigPath(".")
		v.AddConfigPath(fmt.Sprintf("%s/.config/horde", home))
		v.AddConfigPath("/etc/horde")
	} else {
		var extension string
		regex := regexp.MustCompile("((y(a)?ml)|json|toml)$")
		base := filepath.Base(file)
		if regex.MatchString(base) {
			// strip the file type for viper
			parts := strings.Split(filepath.Base(file), ".")
			base = strings.Join(parts[:len(parts)-1], ".")
			extension = parts[len(parts)-1]
		} else {
			return nil, errors.New("configuration does not support that extension type")
		}
		v.SetConfigName(base)
		v.SetConfigType(extension)
		v.SetConfigFile(file)
		v.AddConfigPath(filepath.Dir(file))
	}

	return v, nil
}

// LoadConfigWithDefaults loads configuration with default values.
func LoadConfigWithDefaults(name string, c any, defaults map[string]any) error {
	envPrefix := fmt.Sprintf("HORDE_%s", strings.ToUpper(name))

	v, err := prepare(name)
	if err != nil {
		return err
	}

	// A missing config file is not fatal: defaults and env overrides still
	// apply. Any other read error is surfaced.
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return err
		}
	}

	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	for key, value := range defaults {
		v.SetDefault(key, value)
	}

	if err := v.Unmarshal(&c); err != nil {
		return err
	}

	return nil
}

// LoadConfig reads in a configuration file from a set of locations and
// deserializes it into a Config instance.
func LoadConfig(name string, c any) error {
	home, err := homedir.Dir()
	if err != nil {
		return err
	}

	envPrefix := fmt.Sprintf("HORDE_%s", strings.ToUpper(name))
	envConfig := fmt.Sprintf("%s_CONFIG", envPrefix)

	v := viper.New()

	file := os.Getenv(envConfig)
	if file == "" {
		v.SetConfigName(name)
		v.AddConfigPath(".")
		v.AddConfigPath(fmt.Sprintf("%s/.config/horde", home))
		v.AddConfigPath("/etc/horde")
	} else {
		var extension string
		regex := regexp.MustCompile("((y(a)?ml)|json|toml)$")
		base := filepath.Base(file)
		if regex.MatchString(base) {
			// strip the file type for viper
			parts := strings.Split(filepath.Base(file), ".")
			base = strings.Join(parts[:len(parts)-1], ".")
			extension = parts[len(parts)-1]
		} else {
			return errors.New("configuration does not support that extension type")
		}
		v.SetConfigName(base)
		v.SetConfigType(extension)
		v.SetConfigFile(file)
		v.AddConfigPath(filepath.Dir(file))
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return err
		}
	}

	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.Unmarshal(&c); err != nil {
		return err
	}

	return nil
}

// MarshalConfig converts a config instance to a JSON string.
func MarshalConfig(c any) (string, error) {
	bytes, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

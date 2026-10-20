// Package config parses runtime configuration from the environment.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Config is the validated runtime configuration.
type Config struct {
	ListenAddr    string
	DataDir       string
	SessionSecure bool
}

const (
	EnvListenAddr    = "PROXYWARD_LISTEN"
	EnvDataDir       = "PROXYWARD_DATA_DIR"
	EnvSessionSecure = "PROXYWARD_SESSION_SECURE"

	DefaultListenAddr = "127.0.0.1:8080"
	DefaultDataDir    = "data"
)

// FromEnv builds a Config from getenv, applying defaults and bounds.
func FromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		ListenAddr: DefaultListenAddr,
		DataDir:    DefaultDataDir,
	}
	if v := strings.TrimSpace(getenv(EnvListenAddr)); v != "" {
		cfg.ListenAddr = v
	}
	if v := strings.TrimSpace(getenv(EnvDataDir)); v != "" {
		cfg.DataDir = v
	}
	if v := strings.TrimSpace(getenv(EnvSessionSecure)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a boolean", EnvSessionSecure)
		}
		cfg.SessionSecure = b
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration bounds.
func (c Config) Validate() error {
	_, portStr, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return fmt.Errorf("%s must be host:port", EnvListenAddr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%s port must be between 1 and 65535", EnvListenAddr)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("%s must not be empty", EnvDataDir)
	}
	return nil
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is Switchyard's external configuration, loaded from a JSON file.
type Config struct {
	Listen   string          `json:"listen"`
	Backends []BackendConfig `json:"backends"`
	// Logging is optional. When nil, Switchyard uses its built-in operational
	// log line; when set, each request is logged using the user-defined format.
	Logging *LogConfig `json:"logging"`
	// SetHeaders maps header names to value templates applied to requests
	// forwarded to backends, like nginx's proxy_set_header. Values may contain
	// $variables (see vars.go), e.g. {"X-Real-IP": "$remote_addr"}.
	SetHeaders map[string]string `json:"set_headers"`
}

// BackendConfig describes one configured upstream. URL is required; ID is
// optional and defaults to URL. IDs and URLs must each be unique across
// backends (enforced in newProxy).
type BackendConfig struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// loadConfig reads and parses the configuration file at path.
func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}

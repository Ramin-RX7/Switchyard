package switchyard

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
	// Locations is an optional ordered list of nginx-style location blocks.
	// When present, requests are matched top-to-bottom (first match wins)
	// instead of using the global round-robin over all backends. When absent,
	// behavior is unchanged. See location.go.
	Locations []LocationConfig `json:"locations"`
}

// LocationConfig describes one location block. Path is matched as a prefix
// (the default) or as a Go regexp when Regex is true. Type selects the
// behavior: "proxy" (default) forwards to one of the Backends (referenced by
// id), "static" serves files from Root. Logging and SetHeaders are optional and
// stack with the global ones rather than replacing them.
type LocationConfig struct {
	Path        string            `json:"path"`
	Regex       bool              `json:"regex"`
	Type        string            `json:"type"`         // "proxy" (default) or "static"
	Backends    []string          `json:"backends"`     // backend ids, for type "proxy"
	Root        string            `json:"root"`         // directory, for type "static"
	StripPrefix *string           `json:"strip_prefix"` // nil distinguishes unset from ""
	Logging     *LogConfig        `json:"logging"`
	SetHeaders  map[string]string `json:"set_headers"`
}

// BackendConfig describes one configured upstream. URL is required; ID is
// optional and defaults to URL. IDs and URLs must each be unique across
// backends (enforced in New).
type BackendConfig struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// LoadConfig reads and parses the configuration file at path. It is exported so
// SDK users can reuse the same JSON config format as the turnkey binary.
func LoadConfig(path string) (Config, error) {
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

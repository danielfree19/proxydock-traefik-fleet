// Package traefik_fleet is a Traefik provider plugin that pulls dynamic
// configuration from a central Traefik Fleet Manager API.
//
// The plugin is loaded by Yaegi inside Traefik, so this code stays
// stdlib-only and avoids reflect-heavy or unsafe constructs.
package traefik_fleet

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Config is the provider configuration declared in Traefik's static config
// under `providers.plugin.fleet`.
//
// Yaegi populates this struct from the static config; field names are kept
// simple and use camelCase so the YAML keys read naturally.
type Config struct {
	// Endpoint is the base URL of the manager-api.
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	// FleetID is the fleet this agent belongs to.
	FleetID string `json:"fleetID,omitempty" yaml:"fleetID,omitempty"`
	// AgentID is the agent's stable identifier.
	AgentID string `json:"agentID,omitempty" yaml:"agentID,omitempty"`
	// Token is the bearer token used to authenticate to the manager.
	Token string `json:"token,omitempty" yaml:"token,omitempty"`
	// TokenFile is an optional path containing the bearer token.
	// It takes precedence over Token if set.
	TokenFile string `json:"tokenFile,omitempty" yaml:"tokenFile,omitempty"`
	// PollInterval is how often to fetch config from the manager.
	PollInterval string `json:"pollInterval,omitempty" yaml:"pollInterval,omitempty"`
	// PollTimeout is the per-request timeout for fetches and heartbeats.
	PollTimeout string `json:"pollTimeout,omitempty" yaml:"pollTimeout,omitempty"`
	// CacheFile is the on-disk last-known-good config cache. Empty disables caching.
	CacheFile string `json:"cacheFile,omitempty" yaml:"cacheFile,omitempty"`
	// FailMode controls behavior when the manager is unavailable:
	//   - "keep-last-good": keep applying the last good config (default)
	FailMode string `json:"failMode,omitempty" yaml:"failMode,omitempty"`
	// SigningPublicKey, when set, makes the provider verify the
	// ed25519 signature attached to each /config response before
	// applying. Mismatch / missing signature → keep-last-good and
	// surface the error in the next heartbeat.
	//
	// The value is base64-encoded (the format the manager exposes at
	// GET /api/v1/signing/pubkey).
	SigningPublicKey string `json:"signingPublicKey,omitempty" yaml:"signingPublicKey,omitempty"`
}

// CreateConfig is the symbol Traefik calls to construct a default Config.
func CreateConfig() *Config {
	return &Config{
		PollInterval: "10s",
		PollTimeout:  "5s",
		FailMode:     "keep-last-good",
	}
}

// resolved is the validated, normalized form of Config.
type resolved struct {
	Endpoint         string
	FleetID          string
	AgentID          string
	Token            string
	PollInterval     time.Duration
	PollTimeout      time.Duration
	CacheFile        string
	FailMode         string
	SigningPublicKey string // base64; empty disables verification
}

// validate parses and checks the configuration.
//
// The token is left blank here; tokenFile resolution is done at runtime so
// it can be re-read on rotation.
func (c *Config) validate() (*resolved, error) {
	if c == nil {
		return nil, errors.New("traefik-fleet: nil config")
	}
	r := &resolved{
		Endpoint:         strings.TrimRight(strings.TrimSpace(c.Endpoint), "/"),
		FleetID:          strings.TrimSpace(c.FleetID),
		AgentID:          strings.TrimSpace(c.AgentID),
		Token:            c.Token,
		CacheFile:        c.CacheFile,
		FailMode:         c.FailMode,
		SigningPublicKey: strings.TrimSpace(c.SigningPublicKey),
	}
	if r.Endpoint == "" {
		return nil, errors.New("traefik-fleet: endpoint is required")
	}
	if !strings.HasPrefix(r.Endpoint, "http://") && !strings.HasPrefix(r.Endpoint, "https://") {
		return nil, fmt.Errorf("traefik-fleet: endpoint must start with http:// or https://, got %q", r.Endpoint)
	}
	if r.FleetID == "" {
		return nil, errors.New("traefik-fleet: fleetID is required")
	}
	if r.AgentID == "" {
		return nil, errors.New("traefik-fleet: agentID is required")
	}
	if c.Token == "" && c.TokenFile == "" {
		return nil, errors.New("traefik-fleet: token or tokenFile is required")
	}

	pi, err := parseDuration(c.PollInterval, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("traefik-fleet: invalid pollInterval: %w", err)
	}
	if pi < time.Second {
		return nil, fmt.Errorf("traefik-fleet: pollInterval must be at least 1s, got %s", pi)
	}
	r.PollInterval = pi

	pt, err := parseDuration(c.PollTimeout, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("traefik-fleet: invalid pollTimeout: %w", err)
	}
	r.PollTimeout = pt

	if r.FailMode == "" {
		r.FailMode = "keep-last-good"
	}
	if r.FailMode != "keep-last-good" {
		return nil, fmt.Errorf("traefik-fleet: unsupported failMode %q", r.FailMode)
	}
	return r, nil
}

func parseDuration(s string, def time.Duration) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}

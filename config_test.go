package traefik_fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateConfig_Defaults(t *testing.T) {
	c := CreateConfig()
	if c.PollInterval != "10s" {
		t.Fatalf("PollInterval = %q", c.PollInterval)
	}
	if c.PollTimeout != "5s" {
		t.Fatalf("PollTimeout = %q", c.PollTimeout)
	}
	if c.FailMode != "keep-last-good" {
		t.Fatalf("FailMode = %q", c.FailMode)
	}
}

func TestValidate_Required(t *testing.T) {
	cases := []struct {
		name string
		in   *Config
		want string
	}{
		{"empty", &Config{}, "endpoint is required"},
		{"no scheme", &Config{Endpoint: "manager:8080"}, "must start with http"},
		{"no fleet", &Config{Endpoint: "http://m"}, "fleetID is required"},
		{"no agent", &Config{Endpoint: "http://m", FleetID: "f"}, "agentID is required"},
		{"no token", &Config{Endpoint: "http://m", FleetID: "f", AgentID: "a"}, "token or tokenFile is required"},
		{"bad interval", &Config{Endpoint: "http://m", FleetID: "f", AgentID: "a", Token: "t", PollInterval: "not-a-duration"}, "invalid pollInterval"},
		{"interval too small", &Config{Endpoint: "http://m", FleetID: "f", AgentID: "a", Token: "t", PollInterval: "100ms"}, "at least 1s"},
		{"unsupported failMode", &Config{Endpoint: "http://m", FleetID: "f", AgentID: "a", Token: "t", FailMode: "weird"}, "unsupported failMode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.in.validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidate_OK(t *testing.T) {
	c := &Config{
		Endpoint:     "http://manager-api:8080/",
		FleetID:      "homelab",
		AgentID:      "traefik-1",
		Token:        "dev-token-1",
		PollInterval: "5s",
		PollTimeout:  "3s",
	}
	r, err := c.validate()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Endpoint != "http://manager-api:8080" {
		t.Fatalf("trailing slash not trimmed: %q", r.Endpoint)
	}
	if r.PollInterval != 5*time.Second {
		t.Fatalf("PollInterval = %s", r.PollInterval)
	}
	if r.PollTimeout != 3*time.Second {
		t.Fatalf("PollTimeout = %s", r.PollTimeout)
	}
	if r.FailMode != "keep-last-good" {
		t.Fatalf("FailMode default not applied")
	}
}

func TestLoadToken_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Provider{
		config: &Config{TokenFile: path},
		r:      &resolved{Token: "ignored-when-file-set"},
	}
	tok, err := p.loadToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "secret-token" {
		t.Fatalf("token = %q", tok)
	}
}

func TestLoadToken_FileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Provider{config: &Config{TokenFile: path}, r: &resolved{}}
	if _, err := p.loadToken(); err == nil {
		t.Fatal("expected error for empty token file")
	}
}

func TestLoadToken_Inline(t *testing.T) {
	p := &Provider{config: &Config{Token: "inline"}, r: &resolved{Token: "inline"}}
	tok, err := p.loadToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "inline" {
		t.Fatalf("token = %q", tok)
	}
}

func TestValidateResponse_Bad(t *testing.T) {
	r := &resolved{AgentID: "traefik-1", FleetID: "homelab"}
	cases := []struct {
		name string
		body string
	}{
		{"empty bytes", ""},
		{"null", "null"},
		{"empty object", "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := &configResponse{
				AgentID: r.AgentID,
				FleetID: r.FleetID,
				Config:  []byte(tc.body),
			}
			if err := validateResponse(r, cr); err == nil {
				t.Fatalf("expected error for body %q", tc.body)
			}
		})
	}
}

func TestValidateResponse_AgentMismatch(t *testing.T) {
	r := &resolved{AgentID: "traefik-1", FleetID: "homelab"}
	cr := &configResponse{
		AgentID: "traefik-7",
		FleetID: "homelab",
		Config:  []byte(`{"http":{}}`),
	}
	if err := validateResponse(r, cr); err == nil {
		t.Fatal("expected agent mismatch error")
	}
}

func TestValidateResponse_OK(t *testing.T) {
	r := &resolved{AgentID: "traefik-1", FleetID: "homelab"}
	cr := &configResponse{
		AgentID: "traefik-1",
		FleetID: "homelab",
		Config:  []byte(`{"http":{"routers":{}}}`),
	}
	if err := validateResponse(r, cr); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestSaveLoadCache_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "last-good.json")
	cr := &configResponse{
		FleetID:  "homelab",
		AgentID:  "traefik-1",
		Revision: 42,
		ETag:     `"revision-42"`,
		Config:   []byte(`{"http":{"routers":{"x":{}}}}`),
	}
	if err := saveCache(path, cr, cr.ETag); err != nil {
		t.Fatalf("save: %v", err)
	}
	cf, err := loadCache(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cf == nil {
		t.Fatal("expected cache to be loaded")
	}
	if cf.Revision != 42 {
		t.Fatalf("revision = %d", cf.Revision)
	}
	if cf.ETag != `"revision-42"` {
		t.Fatalf("etag = %q", cf.ETag)
	}
}

func TestLoadCache_Missing(t *testing.T) {
	cf, err := loadCache(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing cache should not error: %v", err)
	}
	if cf != nil {
		t.Fatalf("cf = %+v", cf)
	}
}

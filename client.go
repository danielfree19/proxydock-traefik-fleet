package traefik_fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// configResponse mirrors the manager-api response body.
type configResponse struct {
	FleetID      string          `json:"fleet_id"`
	AgentID      string          `json:"agent_id"`
	Revision     int             `json:"revision"`
	ETag         string          `json:"etag"`
	GeneratedAt  time.Time       `json:"generated_at"`
	Config       json.RawMessage `json:"config"`
	Signature    string          `json:"signature,omitempty"`
	SignatureAlg string          `json:"signature_alg,omitempty"`
}

// fetchResult is what one polling cycle returns.
type fetchResult struct {
	NotModified bool
	Response    *configResponse
	ETag        string
}

// fetchConfig issues a single GET /api/v1/agents/{id}/config call.
//
// If the server returns 304 Not Modified the result indicates no change;
// otherwise the parsed body is returned.
func (p *Provider) fetchConfig(ctx context.Context, ifNoneMatch string) (*fetchResult, error) {
	url := fmt.Sprintf("%s/api/v1/agents/%s/config", p.r.Endpoint, p.r.AgentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	tok, err := p.loadToken()
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return &fetchResult{NotModified: true, ETag: resp.Header.Get("ETag")}, nil
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		var cr configResponse
		if err := json.Unmarshal(body, &cr); err != nil {
			return nil, fmt.Errorf("decode body: %w", err)
		}
		if err := validateResponse(p.r, &cr); err != nil {
			return nil, err
		}
		if err := p.verifySignature([]byte(cr.Config), cr.Signature, cr.SignatureAlg); err != nil {
			return nil, fmt.Errorf("signature verify: %w", err)
		}
		etag := resp.Header.Get("ETag")
		if etag == "" {
			etag = cr.ETag
		}
		return &fetchResult{Response: &cr, ETag: etag}, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("auth failed: %s", resp.Status)
	default:
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
}

// heartbeat reports the current state to the manager.
//
// Heartbeats are best-effort: failures are logged but never block the
// polling loop or affect applied config.
func (p *Provider) heartbeat(ctx context.Context, currentRevision int, lastErr string) {
	url := fmt.Sprintf("%s/api/v1/agents/%s/heartbeat", p.r.Endpoint, p.r.AgentID)
	body := map[string]any{
		"agent_id":         p.r.AgentID,
		"current_revision": currentRevision,
		"provider_version": providerVersion,
		"traefik_version":  os.Getenv("TRAEFIK_VERSION"),
	}
	if lastErr != "" {
		body["last_error"] = lastErr
	}
	buf, err := json.Marshal(body)
	if err != nil {
		p.logf("heartbeat: marshal failed: %v", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		p.logf("heartbeat: build request failed: %v", err)
		return
	}
	tok, err := p.loadToken()
	if err != nil {
		p.logf("heartbeat: load token failed: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		p.logf("heartbeat: request failed: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		p.logf("heartbeat: unexpected status %s", resp.Status)
		return
	}
}

// loadToken returns the current bearer token, preferring TokenFile over
// the inline Token so rotated secrets are picked up on the next poll.
func (p *Provider) loadToken() (string, error) {
	if p.config.TokenFile != "" {
		b, err := os.ReadFile(p.config.TokenFile)
		if err != nil {
			return "", err
		}
		t := strings.TrimSpace(string(b))
		if t == "" {
			return "", errors.New("token file is empty")
		}
		return t, nil
	}
	if p.r.Token != "" {
		return p.r.Token, nil
	}
	return "", errors.New("no token configured")
}

// validateResponse rejects responses that look wrong for this agent.
//
// An empty config is treated as invalid in Phase 0 to avoid accidentally
// blanking out routing if the manager mis-publishes.
func validateResponse(r *resolved, cr *configResponse) error {
	if cr.AgentID != "" && cr.AgentID != r.AgentID {
		return fmt.Errorf("agent_id mismatch: got %q, expected %q", cr.AgentID, r.AgentID)
	}
	if cr.FleetID != "" && cr.FleetID != r.FleetID {
		return fmt.Errorf("fleet_id mismatch: got %q, expected %q", cr.FleetID, r.FleetID)
	}
	if len(cr.Config) == 0 || bytes.Equal(bytes.TrimSpace(cr.Config), []byte("null")) {
		return errors.New("empty config payload")
	}
	if bytes.Equal(bytes.TrimSpace(cr.Config), []byte("{}")) {
		return errors.New("empty config object")
	}
	return nil
}

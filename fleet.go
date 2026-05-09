package traefik_fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// providerVersion identifies the plugin in heartbeat reports and logs.
const providerVersion = "0.1.0"

// Provider is a Traefik provider plugin instance.
//
// One Provider is constructed per static-config block.
type Provider struct {
	name   string
	config *Config
	r      *resolved

	http *http.Client

	mu          sync.Mutex
	lastETag    string
	lastApplied int

	cancel context.CancelFunc
	done   chan struct{}
}

// New is the symbol Traefik calls to instantiate the provider.
//
// It only stores configuration; network calls happen in Provide so that a
// transient manager outage at startup doesn't fail Traefik's boot.
func New(_ context.Context, cfg *Config, name string) (*Provider, error) {
	if cfg == nil {
		return nil, errors.New("traefik-fleet: nil config")
	}
	r, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &Provider{
		name:   name,
		config: cfg,
		r:      r,
		http: &http.Client{
			Timeout: r.PollTimeout,
		},
	}, nil
}

// Init is called once before Provide. We use it to confirm the token is
// readable and to log effective settings.
func (p *Provider) Init() error {
	if _, err := p.loadToken(); err != nil {
		return fmt.Errorf("traefik-fleet: %w", err)
	}
	p.logf("init endpoint=%s fleet=%s agent=%s pollInterval=%s pollTimeout=%s cacheFile=%q failMode=%s",
		p.r.Endpoint, p.r.FleetID, p.r.AgentID, p.r.PollInterval, p.r.PollTimeout, p.r.CacheFile, p.r.FailMode)
	return nil
}

// Provide starts the polling goroutine and returns immediately.
//
// The goroutine pushes a json.Marshaler onto cfgChan whenever the manager
// returns a new revision (or whenever the on-disk cache is newer than what
// has been applied so far in this process).
func (p *Provider) Provide(cfgChan chan<- json.Marshaler) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})

	go p.run(ctx, cfgChan)
	return nil
}

// Stop shuts down the polling goroutine. It blocks briefly waiting for the
// goroutine to exit so callers can reason about resource cleanup.
func (p *Provider) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// run is the polling loop.
//
// On startup it attempts to apply the on-disk last-known-good config so
// Traefik can route immediately even if the manager is slow or down. After
// that it polls on the configured interval, sends a heartbeat each cycle,
// and persists every successful revision to the cache.
func (p *Provider) run(ctx context.Context, cfgChan chan<- json.Marshaler) {
	defer close(p.done)

	if cf, err := loadCache(p.r.CacheFile); err != nil {
		p.logf("cache load error: %v", err)
	} else if cf != nil {
		var cr configResponse
		if err := json.Unmarshal(cf.Response, &cr); err != nil {
			p.logf("cache decode error: %v", err)
		} else if err := validateResponse(p.r, &cr); err != nil {
			p.logf("cache validation error: %v", err)
		} else {
			p.logf("applying cached revision %d (saved_at=%s)", cf.Revision, cf.SavedAt.Format(time.RFC3339))
			p.send(cfgChan, &cr)
			p.mu.Lock()
			p.lastETag = cf.ETag
			p.lastApplied = cr.Revision
			p.mu.Unlock()
		}
	}

	// First poll runs immediately so the demo doesn't wait pollInterval
	// before doing anything visible.
	p.poll(ctx, cfgChan)

	t := time.NewTicker(p.r.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.poll(ctx, cfgChan)
		}
	}
}

// poll runs a single fetch + heartbeat cycle.
func (p *Provider) poll(parent context.Context, cfgChan chan<- json.Marshaler) {
	ctx, cancel := context.WithTimeout(parent, p.r.PollTimeout+2*time.Second)
	defer cancel()

	p.mu.Lock()
	prevETag := p.lastETag
	prevRev := p.lastApplied
	p.mu.Unlock()

	res, err := p.fetchConfig(ctx, prevETag)
	lastErr := ""
	if err != nil {
		lastErr = err.Error()
		p.logf("fetch error (failMode=%s, keeping last-good): %v", p.r.FailMode, err)
		// keep-last-good: do not push an empty config; reuse the heartbeat
		// to surface the error to the manager.
		p.heartbeat(ctx, prevRev, lastErr)
		return
	}

	if res.NotModified {
		p.logf("config unchanged (revision=%d etag=%s)", prevRev, prevETag)
		p.heartbeat(ctx, prevRev, "")
		return
	}

	p.send(cfgChan, res.Response)

	p.mu.Lock()
	p.lastETag = res.ETag
	p.lastApplied = res.Response.Revision
	p.mu.Unlock()

	if err := saveCache(p.r.CacheFile, res.Response, res.ETag); err != nil {
		p.logf("cache save error: %v", err)
	}

	p.logf("applied revision=%d etag=%s", res.Response.Revision, res.ETag)
	p.heartbeat(ctx, res.Response.Revision, "")
}

// send pushes the config payload onto cfgChan, respecting cancellation.
//
// We marshal the json.RawMessage directly so the payload Traefik receives
// is byte-for-byte what the manager returned.
func (p *Provider) send(cfgChan chan<- json.Marshaler, cr *configResponse) {
	msg := json.RawMessage(cr.Config)
	select {
	case cfgChan <- msg:
	case <-time.After(p.r.PollTimeout):
		p.logf("warn: timed out sending config to Traefik runtime")
	}
}

// logf writes a prefixed line to stdout.
//
// We avoid log/slog and the stdlib log package's default flags so the
// output is consistent and Yaegi-friendly across Traefik versions.
func (p *Provider) logf(format string, args ...any) {
	prefix := fmt.Sprintf("[traefik-fleet %s agent=%s] ", p.name, p.r.AgentID)
	fmt.Fprintf(os.Stdout, prefix+format+"\n", args...)
}

# traefik-fleet provider plugin

A pull-based [Traefik](https://traefik.io) [provider plugin](https://plugins.traefik.io/install)
that fetches dynamic configuration from a central Traefik Fleet Manager.

## What it does

On a configurable interval the plugin:

1. `GET`s `/{endpoint}/api/v1/agents/{agentID}/config` with a bearer token
   and an `If-None-Match` header carrying the last applied ETag.
2. On `304 Not Modified` it does nothing (and reports a heartbeat).
3. On `200 OK` it validates the payload, sends the dynamic config to
   Traefik's config channel, and persists the response to a
   last-known-good cache file.
4. `POST`s a heartbeat to `/api/v1/agents/{agentID}/heartbeat` reporting
   the currently applied revision and any error from the cycle.

When the manager is unavailable the plugin keeps the last applied
configuration and surfaces the failure via the heartbeat once the manager
is back.

## Static config

```yaml
experimental:
  localPlugins:
    fleet:
      moduleName: github.com/danielfree19/proxydock-traefik-fleet

providers:
  plugin:
    fleet:
      endpoint: "http://manager-api:8080"
      fleetID: "homelab"
      agentID: "traefik-1"
      tokenFile: "/run/secrets/traefik-fleet-token"
      pollInterval: "5s"
      pollTimeout: "5s"
      cacheFile: "/var/lib/traefik-fleet/last-good.json"
      failMode: "keep-last-good"
```

`token` may be used inline instead of `tokenFile` for development.

## Limitations (Phase 0)

- One revision at a time, no rollback on the agent side.
- `failMode` only supports `keep-last-good`.
- Empty / null config payloads are rejected (no way to intentionally
  deconfigure all routes through this provider yet).
- The plugin runs under Yaegi, so uses only the Go standard library.

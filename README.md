# voodu-caddy

Official Voodu ingress plugin, backed by [Caddy](https://caddyserver.com)'s
Admin API.

The plugin reconciles `ingress` manifests — one `host` → `service:port`
route per manifest — into a Caddy config loaded atomically via `POST
/load`. Automatic HTTPS via Let's Encrypt is a TLS-enabled knob away.

## Install

```sh
voodu plugins:install github.com/thadeu/voodu-caddy
```

The `install` script downloads Caddy, writes a systemd unit, and
starts `voodu-caddy.service` listening on `127.0.0.1:2019` (Admin API)
plus `:80` / `:443` (ingress).

## Usage

The plugin is invoked by the Voodu controller, not directly by users.
Operators interact with it through `ingress` manifests.

```sh
voodu apply -f ingress.hcl -a prod
```

The controller exports `VOODU_INGRESS_*` env vars and calls the
plugin's `apply` command. The plugin writes the route to
`/opt/voodu/caddy/routes/<app>.json` and POSTs a rebuilt config to
Caddy's Admin API.

## Upstream resolution

`host` and `service` are required on every ingress. `port` is optional
— when omitted, the controller fills it in by looking up a matching
`service "<name>" {}` block (takes `port`) or, failing that, a
`deployment "<name>" {}` block (takes the container side of the first
entry in `ports = [...]`).

The target must exist in the controller's store at reconcile time. An
ingress that names a non-applied service/deployment is marked transient
and retried on backoff — apply the deployment first, then the ingress,
or apply both in the same `voodu apply -f` call. Typos eventually
surface as a non-recoverable error once retries exhaust.

This also means port-in-two-places duplication goes away for the
common case:

```hcl
deployment "api" {
  image = "ghcr.io/you/api:latest"
  ports = ["3000"]
}

ingress "api" {
  host    = "api.example.com"
  service = "api"
  # port inferred: 3000
}
```

## TLS profiles

Four shapes of `tls {}` are supported. All share `host`, `service`,
and optionally `port` at the outer `ingress` block — what differs is
the TLS block.

### 1. HTTP only — no `tls` block

```hcl
ingress "api" {
  host    = "api.internal"
  service = "api"
  port    = 3000
}
```

Caddy serves plain HTTP on `:80`, upstream `api:3000`. Use for internal
services, dev, or setups where something in front (LB, CDN) already
terminates TLS.

### 2. Public TLS via Let's Encrypt (ACME HTTP-01)

```hcl
ingress "api" {
  host    = "api.clowk.in"
  service = "api"
  port    = 3000

  tls {
    enabled  = true
    provider = "letsencrypt"
    email    = "ops@clowk.in"
  }
}
```

Finite, known list of hosts. Caddy issues a cert over HTTP-01 on boot;
HTTP→HTTPS redirect is automatic. Multiple ingresses sharing `email`
reuse the same ACME account (policies are grouped by `(provider,
email, on_demand)` in `internal/ingress/config.go`).

**Does not support wildcards.** HTTP-01 cannot validate `*.example.com`
— use profile 4 for wildcard.

### 3. Internal CA (self-signed by Caddy)

```hcl
ingress "api" {
  host    = "api.dev.local"
  service = "api"
  port    = 3000

  tls {
    enabled  = true
    provider = "internal"
  }
}
```

Caddy mints its own root CA and issues a cert locally. Browsers warn
until the CA is trusted. Useful for dev/staging without a public
domain.

### 4. On-demand TLS with `ask` callback (multi-tenant wildcard)

```hcl
ingress "tenants" {
  host    = "*.clowk.in"
  service = "app"
  port    = 3000

  tls {
    enabled   = true
    provider  = "letsencrypt"
    email     = "ssl@clowk.in"
    on_demand = true
    ask       = "http://app:3000/internal/allow_domain"
  }
}
```

The only profile that accepts true wildcards. No cert is minted
upfront. When a new SNI hits `:443`, Caddy calls `ask` with
`?domain=<hostname>` — your app returns `200` if the hostname is a
valid tenant, `404` otherwise. Only approved hostnames trigger ACME
issuance.

`ask` is **required** when `on_demand = true`. Without it the plugin
would be an open cert-issuance proxy and would hit Let's Encrypt rate
limits in minutes. The app endpoint is your responsibility — in Rails,
something like:

```ruby
# app/controllers/internal_controller.rb
def allow_domain
  domain = params[:domain]
  return head :ok if Tenant.exists?(custom_domain: domain)
  head :not_found
end
```

### Combining profiles

Canonical multi-tenant setup: a fixed marketing/API host plus a
wildcard for customer subdomains, both sharing one ACME account.

```hcl
ingress "api" {
  host    = "api.clowk.in"
  service = "api"
  port    = 3000
  tls { enabled = true  provider = "letsencrypt"  email = "ssl@clowk.in" }
}

ingress "tenants" {
  host    = "*.clowk.in"
  service = "app"
  port    = 3000
  tls {
    enabled   = true
    provider  = "letsencrypt"
    email     = "ssl@clowk.in"
    on_demand = true
    ask       = "http://app:3000/internal/allow_domain"
  }
}
```

## What is not supported (yet)

| Case                                           | Status |
|------------------------------------------------|--------|
| DNS-01 wildcard without on-demand              | ❌     |
| On-demand without an `ask` endpoint            | ❌ (by design — see below) |
| Path/header/method matchers beyond `host`      | ❌     |
| Redirects / rewrites                           | ❌     |
| Middleware (rate-limit, basic auth, headers)   | ❌     |

**Why no "allow all" shortcut for on-demand:** it is tempting to skip
the `ask` callback when the tenancy check is implicit (single-app
installs, trusted infra). We deliberately do not support this. Early
experience on the reference stack showed SSL activation races and
rate-limit hits when there was no gate — the app-driven `ask` plus a
retry job was the only configuration that stayed green. Keeping `ask`
required pushes operators toward the pattern that actually works.

## Commands

| Command  | Env (primary)                                             | Effect |
|----------|-----------------------------------------------------------|--------|
| `apply`  | `VOODU_APP`, `VOODU_INGRESS_HOST`, `VOODU_INGRESS_SERVICE` | upsert route + reload Caddy |
| `remove` | `VOODU_APP`                                               | delete route + reload Caddy |
| `list`   | —                                                         | JSON list of routes on disk |
| `reload` | —                                                         | rebuild Caddy config from on-disk state |

Every command emits a JSON envelope on stdout:

```json
{"status": "ok", "data": { "url": "https://api.example.com", ... }}
```

## State

```
/opt/voodu/caddy/
├── bin/          # plugin binary + command wrappers
├── routes/       # one <app>.json per ingress
├── data/         # caddy XDG_DATA_HOME (ACME certs, etc.)
├── config/       # caddy XDG_CONFIG_HOME
└── empty.json    # bootstrap config loaded by systemd unit
```

`reload` rebuilds the full Caddy config from `routes/` and replaces
the running config atomically. On restart, `caddy run --resume`
re-plays the last accepted config from Caddy's own autosave — so the
service comes back without the plugin doing anything.

## Development

```sh
make build            # bin/voodu-caddy
make test             # unit + e2e (spawns binary against httptest)
make cross            # dist/voodu-caddy_linux_{amd64,arm64}
```

The end-to-end test (`cmd/voodu-caddy/main_test.go`) builds the
binary, spins up a mock Admin API with `httptest`, and walks the
full `apply → list → remove` cycle.

## License

MIT.

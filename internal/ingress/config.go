package ingress

import "fmt"

// BuildCaddyConfig turns a list of Routes into the blob accepted by
// Caddy's POST /load. Shape:
//
//	{
//	  "apps": {
//	    "http": {
//	      "servers": {
//	        "voodu": {
//	          "listen": [":80", ":443"],
//	          "routes": [ ... one per ingress ... ]
//	        }
//	      }
//	    },
//	    "tls": {
//	      "automation": {
//	        "policies":  [ ... per-(issuer,email,on_demand) ... ],
//	        "on_demand": { "ask": "<callback URL>" }
//	      }
//	    }
//	  }
//	}
//
// The single "voodu" server owns both ports. Caddy auto-selects HTTPS
// when a route has TLS configured; auto-https also injects the HTTP→HTTPS
// redirect. On-demand TLS is wired via a global `on_demand.ask` gate
// plus per-policy `on_demand: true` subjects — this is what lets a
// single wildcard route (`*.tenant.example.com`) serve arbitrary
// subdomains with per-tenant cert issuance gated by the app.
func BuildCaddyConfig(routes []Route) map[string]any {
	httpRoutes := make([]map[string]any, 0, len(routes))

	for _, r := range routes {
		httpRoutes = append(httpRoutes, caddyRoutesFor(r)...)
	}

	cfg := map[string]any{
		// Admin API must be re-declared on every /load because Caddy
		// replaces the full config atomically. Omitting it would let the
		// default (localhost:2019) take over — which, inside the
		// container, is loopback-only and docker's -p 127.0.0.1:2019:2019
		// mapping can't reach it. 0.0.0.0:2019 is safe because docker only
		// exposes the port on the host's loopback.
		"admin": map[string]any{
			"listen": "0.0.0.0:2019",
		},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"voodu": map[string]any{
						"listen": []string{":80", ":443"},
						"routes": httpRoutes,
					},
				},
			},
		},
	}

	if tls := tlsAppConfig(routes); tls != nil {
		cfg["apps"].(map[string]any)["tls"] = tls
	}

	return cfg
}

// caddyRoutesFor expands a Route into one or more Caddy routes. Without
// Locations, a single catch-all route matches the host. With
// Locations, each entry becomes its own route with a path matcher, in
// declaration order. Caddy walks routes top-to-bottom and the first
// match wins (`terminal: true`), so more specific prefixes should come
// first — the controller preserves declaration order, callers declare
// the most specific prefix first.
func caddyRoutesFor(r Route) []map[string]any {
	if len(r.Locations) == 0 {
		return []map[string]any{hostRoute(r)}
	}

	out := make([]map[string]any, 0, len(r.Locations))

	for _, loc := range r.Locations {
		out = append(out, locationRoute(r, loc))
	}

	return out
}

// hostRoute is the catch-all shape: match on host only, reverse_proxy
// everything. Pinned to the fields we actually use so accidental drift
// is loud. Host header is preserved by Caddy v2's reverse_proxy default,
// so we don't set it explicitly — the on-demand ask gate reads the SNI,
// not the upstream Host, so this is safe for multi-tenant routes too.
func hostRoute(r Route) map[string]any {
	return map[string]any{
		"match": []map[string]any{
			{"host": []string{r.Host}},
		},
		"handle":   []map[string]any{reverseProxyHandler(r)},
		"terminal": true,
	}
}

// locationRoute matches host + path prefix. Caddy's path matcher uses
// shell globs, so "/api/v1" matches only exactly that. To match both
// the exact prefix and everything under it ("/api/v1/foo"), we emit
// both patterns. Strip rewrites the request to remove the prefix
// before the upstream sees it.
func locationRoute(r Route, loc Location) map[string]any {
	// A root path or empty string is a catch-all under this host — same
	// shape as hostRoute, so fall through to that.
	if loc.Path == "" || loc.Path == "/" {
		return hostRoute(r)
	}

	match := map[string]any{
		"host": []string{r.Host},
		"path": pathPatterns(loc.Path),
	}

	handlers := []map[string]any{}

	if loc.Strip {
		handlers = append(handlers, map[string]any{
			"handler":           "rewrite",
			"strip_path_prefix": loc.Path,
		})
	}

	handlers = append(handlers, reverseProxyHandler(r))

	return map[string]any{
		"match":    []map[string]any{match},
		"handle":   handlers,
		"terminal": true,
	}
}

// reverseProxyHandler builds a Caddy reverse_proxy block for r.
//
// Upstream selection:
//
//   - If Upstreams has 2+ entries, emit one dial per replica plus
//     load_balancing.selection_policy (default "round_robin").
//   - If Upstreams has exactly 1 entry, emit that — single-upstream
//     routes skip the LB block to keep the config noise-free.
//   - Otherwise fall back to r.Upstream (legacy single-upstream path,
//     still used by older controllers that predate replica awareness).
//
// Active health checks are wired only when LBInterval is set. The probe
// path falls back to "/" — Caddy's default — when HealthCheckPath is
// empty. Passive observation is always on (Caddy default).
func reverseProxyHandler(r Route) map[string]any {
	upstreams := r.Upstreams
	if len(upstreams) == 0 && r.Upstream != "" {
		upstreams = []string{r.Upstream}
	}

	dials := make([]map[string]any, 0, len(upstreams))
	for _, u := range upstreams {
		dials = append(dials, map[string]any{"dial": u})
	}

	h := map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": dials,
	}

	if len(upstreams) > 1 {
		policy := r.LBPolicy
		if policy == "" {
			policy = "round_robin"
		}

		h["load_balancing"] = map[string]any{
			"selection_policy": map[string]any{"policy": policy},
		}
	}

	if r.LBInterval != "" {
		path := r.HealthCheckPath
		if path == "" {
			path = "/"
		}

		h["health_checks"] = map[string]any{
			"active": map[string]any{
				"uri":      path,
				"interval": r.LBInterval,
				"timeout":  r.LBInterval,
			},
		}
	}

	return h
}

// pathPatterns expands "/api/v1" into ["/api/v1", "/api/v1/*"] so both
// the exact prefix and everything beneath it match. Without the second
// pattern, "/api/v1/foo" would not match.
func pathPatterns(p string) []string {
	return []string{p, p + "/*"}
}

// tlsAppConfig assembles the whole `apps.tls` block. Returns nil when
// no route needs TLS at all (pure HTTP fleet).
func tlsAppConfig(routes []Route) map[string]any {
	policies := tlsPolicies(routes)
	ask := onDemandAsk(routes)

	if len(policies) == 0 && ask == "" {
		return nil
	}

	automation := map[string]any{}

	if len(policies) > 0 {
		automation["policies"] = policies
	}

	if ask != "" {
		// Caddy's `on_demand.ask` is global — a single URL gates every
		// on-demand issuance. We take the first non-empty Ask; the plugin
		// boundary enforces that all on-demand routes agree.
		automation["on_demand"] = map[string]any{
			"ask": ask,
		}
	}

	return map[string]any{"automation": automation}
}

// onDemandAsk returns the first non-empty TLSAsk on any on-demand route.
// Caddy supports only one global ask endpoint; callers are expected to
// point every on-demand route at the same URL.
func onDemandAsk(routes []Route) string {
	for _, r := range routes {
		if r.OnDemand && r.TLSAsk != "" {
			return r.TLSAsk
		}
	}

	return ""
}

// tlsPolicies groups routes into automation policies, one per
// (provider, email, on_demand) triple. Routes without a provider and
// without on-demand contribute nothing (HTTP-only or Caddy-internal).
//
// Iteration order is stable: policies appear in the order of the first
// route that contributed to each group, so generated config blobs are
// deterministic for a given Routes slice (which List() already sorts).
func tlsPolicies(routes []Route) []map[string]any {
	type key struct {
		provider string
		email    string
		onDemand bool
	}

	grouped := map[key][]string{}

	var order []key

	for _, r := range routes {
		acme := r.TLSProvider != "" && r.TLSProvider != "internal"

		if !acme && !r.OnDemand {
			continue
		}

		k := key{provider: r.TLSProvider, email: r.TLSEmail, onDemand: r.OnDemand}

		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}

		grouped[k] = append(grouped[k], r.Host)
	}

	out := make([]map[string]any, 0, len(order))

	for _, k := range order {
		policy := map[string]any{
			"subjects": grouped[k],
		}

		if k.onDemand {
			policy["on_demand"] = true
		}

		if k.provider != "" && k.provider != "internal" {
			policy["issuers"] = []map[string]any{acmeIssuer(k.provider, k.email)}
		}

		out = append(out, policy)
	}

	return out
}

// acmeIssuer returns the Caddy issuer blob for a provider name. Today
// only "letsencrypt" has real handling; unknown providers fall back to
// a plain ACME entry so operators can add new providers in HCL without
// the plugin blocking them.
func acmeIssuer(provider, email string) map[string]any {
	issuer := map[string]any{
		"module": "acme",
	}

	if email != "" {
		issuer["email"] = email
	}

	// letsencrypt is Caddy's default directory, so leaving "ca" empty is
	// equivalent — but being explicit helps when operators read dumps.
	if provider == "letsencrypt" {
		issuer["ca"] = "https://acme-v02.api.letsencrypt.org/directory"
	}

	return issuer
}

// UpstreamForPort composes a host:port upstream from a service name and
// a port. Today we resolve service name literally — it's expected to be
// reachable as a DNS name inside the Docker network (that's how Gokku's
// networking works). If the port is zero, default to 80 (HTTP plaintext
// between ingress and upstream is fine since both are on the same host).
func UpstreamForPort(service string, port int) (string, error) {
	if service == "" {
		return "", fmt.Errorf("service is required")
	}

	if port <= 0 {
		port = 80
	}

	return fmt.Sprintf("%s:%d", service, port), nil
}

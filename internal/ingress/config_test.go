package ingress

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCaddyConfig_Basic(t *testing.T) {
	cfg := BuildCaddyConfig([]Route{
		{App: "api", Host: "api.example.com", Upstream: "api:3000"},
	})

	blob := marshal(t, cfg)

	// Admin API must always be present — /load replaces the whole config
	// atomically, so if we omit it Caddy resets admin to loopback-only and
	// the plugin can't talk back.
	mustContain(t, blob, `"admin":{"listen":"0.0.0.0:2019"}`)
	mustContain(t, blob, `"listen":[":80",":443"]`)
	mustContain(t, blob, `"host":["api.example.com"]`)
	mustContain(t, blob, `"dial":"api:3000"`)
	mustContain(t, blob, `"handler":"reverse_proxy"`)

	// No TLS policies when no ACME routes.
	if strings.Contains(blob, `"automation"`) {
		t.Errorf("unexpected tls automation without acme routes: %s", blob)
	}
}

func TestBuildCaddyConfig_TLSGroupsByProviderAndEmail(t *testing.T) {
	routes := []Route{
		{App: "api", Host: "api.x.com", Upstream: "api:3000", TLSProvider: "letsencrypt", TLSEmail: "ops@x.com"},
		{App: "web", Host: "x.com", Upstream: "web:8080", TLSProvider: "letsencrypt", TLSEmail: "ops@x.com"},
		{App: "admin", Host: "admin.x.com", Upstream: "admin:9000", TLSProvider: "letsencrypt", TLSEmail: "sec@x.com"},
		{App: "local", Host: "local.x.com", Upstream: "local:80"},
	}

	blob := marshal(t, BuildCaddyConfig(routes))

	// Two policies expected: one shared by api+web (ops@), one for admin (sec@).
	// local.x.com contributes none since it has no provider.
	if !strings.Contains(blob, `"ops@x.com"`) {
		t.Errorf("ops@ email missing: %s", blob)
	}

	if !strings.Contains(blob, `"sec@x.com"`) {
		t.Errorf("sec@ email missing: %s", blob)
	}

	if !strings.Contains(blob, "acme-v02.api.letsencrypt.org") {
		t.Errorf("letsencrypt CA endpoint missing: %s", blob)
	}
}

func TestBuildCaddyConfig_InternalProviderSkipsPolicy(t *testing.T) {
	cfg := BuildCaddyConfig([]Route{
		{App: "api", Host: "dev.local", Upstream: "api:80", TLSProvider: "internal"},
	})

	blob := marshal(t, cfg)

	// "internal" is Caddy's self-signed; no automation policy needed.
	if strings.Contains(blob, `"automation"`) {
		t.Errorf("internal provider should not produce automation block: %s", blob)
	}
}

func TestBuildCaddyConfig_OnDemandWildcard(t *testing.T) {
	routes := []Route{
		{
			App:         "tenants",
			Host:        "*.clowk.in",
			Upstream:    "app:3000",
			TLSProvider: "letsencrypt",
			TLSEmail:    "ssl@clowk.dev",
			OnDemand:    true,
			TLSAsk:      "http://app:3000/internal/allow_domain",
		},
	}

	blob := marshal(t, BuildCaddyConfig(routes))

	mustContain(t, blob, `"host":["*.clowk.in"]`)
	mustContain(t, blob, `"on_demand":true`)
	mustContain(t, blob, `"ask":"http://app:3000/internal/allow_domain"`)
	mustContain(t, blob, `"ssl@clowk.dev"`)

	// Wildcard subject still gets issued via the configured ACME provider —
	// that's the whole point of on-demand + letsencrypt (no DNS-01 needed).
	mustContain(t, blob, `"subjects":["*.clowk.in"]`)
}

func TestBuildCaddyConfig_OnDemandAndStaticCoexist(t *testing.T) {
	routes := []Route{
		{App: "api", Host: "api.clowk.in", Upstream: "api:3000", TLSProvider: "letsencrypt", TLSEmail: "ssl@clowk.dev"},
		{
			App: "tenants", Host: "*.clowk.in", Upstream: "app:3000",
			TLSProvider: "letsencrypt", TLSEmail: "ssl@clowk.dev",
			OnDemand: true, TLSAsk: "http://app:3000/internal/allow_domain",
		},
	}

	blob := marshal(t, BuildCaddyConfig(routes))

	// Two separate policies: static (ACME only) and on-demand (ACME + ask).
	// Shared email means the same ACME account is reused across both.
	if strings.Count(blob, `"subjects":`) != 2 {
		t.Errorf("expected 2 policy subjects blocks, got:\n%s", blob)
	}

	if !strings.Contains(blob, `"on_demand":true`) {
		t.Errorf("on-demand policy flag missing: %s", blob)
	}

	// The ask URL is global under automation.on_demand.ask, not inside a policy.
	mustContain(t, blob, `"automation":{`)
	mustContain(t, blob, `"ask":"http://app:3000/internal/allow_domain"`)
}

func TestBuildCaddyConfig_OnDemandWithoutProviderEmitsPolicy(t *testing.T) {
	routes := []Route{
		{
			App: "tenants", Host: "*.example.com", Upstream: "app:80",
			OnDemand: true, TLSAsk: "http://app/ask",
		},
	}

	blob := marshal(t, BuildCaddyConfig(routes))

	// Even without an explicit provider, on-demand still needs a policy
	// so Caddy picks up the on_demand flag for these subjects.
	mustContain(t, blob, `"on_demand":true`)
	mustContain(t, blob, `"subjects":["*.example.com"]`)
}

func TestBuildCaddyConfig_LocationsPreserveAndStrip(t *testing.T) {
	routes := []Route{
		{
			App:      "api",
			Host:     "api.example.com",
			Upstream: "api:3000",
			Locations: []Location{
				{Path: "/api/v1"},
				{Path: "/api/v2"},
			},
		},
		{
			App:      "docs",
			Host:     "example.com",
			Upstream: "docs:80",
			Locations: []Location{
				{Path: "/docs/voodu", Strip: true},
			},
		},
	}

	blob := marshal(t, BuildCaddyConfig(routes))

	// Two routes for api (v1, v2) + one for docs → 3 routes total under
	// the voodu server. Each emits its own "terminal":true entry, so
	// count those as a proxy for route count.
	if got := strings.Count(blob, `"terminal":true`); got != 3 {
		t.Errorf("expected 3 terminal routes (2 api locations + 1 docs), got %d: %s", got, blob)
	}

	// Both patterns emitted per location — exact prefix + subpaths.
	mustContain(t, blob, `"path":["/api/v1","/api/v1/*"]`)
	mustContain(t, blob, `"path":["/api/v2","/api/v2/*"]`)
	mustContain(t, blob, `"path":["/docs/voodu","/docs/voodu/*"]`)

	// Strip emits a rewrite handler BEFORE reverse_proxy.
	mustContain(t, blob, `"strip_path_prefix":"/docs/voodu"`)

	// Non-strip locations must NOT get a rewrite handler (otherwise the
	// upstream would see a mangled URI).
	if strings.Contains(blob, `"strip_path_prefix":"/api/v1"`) {
		t.Errorf("unexpected rewrite on non-strip location: %s", blob)
	}
}

func TestBuildCaddyConfig_RootPathIsCatchAll(t *testing.T) {
	// path = "/" or "" collapses to a host-only route. Keeps the HCL
	// `location { path = "/" }` shape equivalent to omitting location
	// entirely, without surprising the operator.
	routes := []Route{
		{
			App: "api", Host: "api.example.com", Upstream: "api:3000",
			Locations: []Location{{Path: "/"}},
		},
	}

	blob := marshal(t, BuildCaddyConfig(routes))

	if strings.Contains(blob, `"path"`) {
		t.Errorf("root path should not emit a path matcher: %s", blob)
	}

	mustContain(t, blob, `"host":["api.example.com"]`)
}

func TestBuildCaddyConfig_VersionedAPIFanOut(t *testing.T) {
	// Two services sharing a host, each owning a path prefix — the
	// canonical versioned-API example from the docs.
	routes := []Route{
		{
			App: "api-v1", Host: "api.example.com", Upstream: "api-v1:3000",
			Locations: []Location{{Path: "/api/v1"}},
		},
		{
			App: "api-v2", Host: "api.example.com", Upstream: "api-v2:3000",
			Locations: []Location{{Path: "/api/v2"}},
		},
	}

	blob := marshal(t, BuildCaddyConfig(routes))

	mustContain(t, blob, `"dial":"api-v1:3000"`)
	mustContain(t, blob, `"dial":"api-v2:3000"`)
	mustContain(t, blob, `"path":["/api/v1","/api/v1/*"]`)
	mustContain(t, blob, `"path":["/api/v2","/api/v2/*"]`)
}

func TestUpstreamForPort(t *testing.T) {
	up, err := UpstreamForPort("api", 3000)
	if err != nil || up != "api:3000" {
		t.Errorf("got (%q, %v)", up, err)
	}

	// Zero port defaults to 80 — accessories listening on stdin docker
	// don't always expose a port explicitly.
	up, _ = UpstreamForPort("api", 0)
	if up != "api:80" {
		t.Errorf("default port: %q", up)
	}

	if _, err := UpstreamForPort("", 80); err == nil {
		t.Error("empty service should error")
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func mustContain(t *testing.T, blob, substr string) {
	t.Helper()

	if !strings.Contains(blob, substr) {
		t.Errorf("missing %q in:\n%s", substr, blob)
	}
}

package ingress

import (
	"strings"
	"testing"
)

func tcpRoute(app, host string) Route {
	return Route{App: app, Host: host, Upstream: app + "-0:7419", TLSProvider: "letsencrypt", TCPListen: 7419}
}

func TestBuildCaddyConfig_TCPRoutesGoToLayer4(t *testing.T) {
	cfg := BuildCaddyConfig([]Route{
		{App: "web", Host: "web.example.com", Upstream: "web:3000"},
		tcpRoute("paris", "paris.eu-de-1.anway.clowk.in"),
		tcpRoute("roma", "roma.eu-de-1.anway.clowk.in"),
	})

	blob := marshal(t, cfg)

	mustContain(t, blob, `"tcp-7419":{"listen":[":7419"]`)
	mustContain(t, blob, `"sni":["paris.eu-de-1.anway.clowk.in"]`)
	mustContain(t, blob, `"sni":["roma.eu-de-1.anway.clowk.in"]`)
	mustContain(t, blob, `"dial":["paris-0:7419"]`)
	mustContain(t, blob, `{"handler":"tls"},{"handler":"proxy"`)
	mustContain(t, blob, `"automate":["paris.eu-de-1.anway.clowk.in","roma.eu-de-1.anway.clowk.in"]`)

	httpRoutes := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["voodu"].(map[string]any)["routes"].([]map[string]any)

	if len(httpRoutes) != 1 {
		t.Errorf("a tcp route must not become an http route: %d http routes", len(httpRoutes))
	}
}

func TestBuildCaddyConfig_NoLayer4WithoutTCPRoutes(t *testing.T) {
	blob := marshal(t, BuildCaddyConfig([]Route{{App: "web", Host: "web.example.com", Upstream: "web:3000"}}))

	if strings.Contains(blob, `"layer4"`) || strings.Contains(blob, `"automate"`) {
		t.Errorf("an http-only fleet must get the same config as before: %s", blob)
	}
}

func TestBuildCaddyConfig_TCPPortsGetOneServerEach(t *testing.T) {
	other := tcpRoute("pg", "pg.example.com")
	other.TCPListen = 5433

	blob := marshal(t, BuildCaddyConfig([]Route{tcpRoute("paris", "paris.example.com"), other}))

	mustContain(t, blob, `"tcp-5433":{"listen":[":5433"]`)
	mustContain(t, blob, `"tcp-7419":{"listen":[":7419"]`)
}

func TestBuildCaddyConfig_TCPInternalProviderUsesCaddyCA(t *testing.T) {
	r := tcpRoute("paris", "paris.test")
	r.TLSProvider = "internal"

	blob := marshal(t, BuildCaddyConfig([]Route{r}))

	mustContain(t, blob, `{"issuers":[{"module":"internal"}],"subjects":["paris.test"]}`)
}

func TestBuildCaddyConfig_TCPReplicasAreBalanced(t *testing.T) {
	r := tcpRoute("esl", "esl.example.com")
	r.Upstreams = []string{"esl-0:8084", "esl-1:8084"}

	blob := marshal(t, BuildCaddyConfig([]Route{r}))

	mustContain(t, blob, `"upstreams":[{"dial":["esl-0:8084"]},{"dial":["esl-1:8084"]}]`)
	mustContain(t, blob, `"load_balancing":{"selection":{"policy":"round_robin"}}`)
}

func TestRouteValidate_TCPRefusesWhatCaddyCannotServe(t *testing.T) {
	cases := map[string]func(*Route){
		"no tls":    func(r *Route) { r.TLSProvider = "" },
		"locations": func(r *Route) { r.Locations = []Location{{Path: "/x"}} },
		"on demand": func(r *Route) { r.OnDemand, r.TLSAsk = true, "http://app/ask" },
		"wildcard":  func(r *Route) { r.Host = "*.example.com" },
		"bad port":  func(r *Route) { r.TCPListen = 70000 },
	}

	for name, change := range cases {
		r := tcpRoute("paris", "paris.example.com")

		change(&r)

		if err := r.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	if err := tcpRoute("paris", "paris.example.com").Validate(); err != nil {
		t.Errorf("a plain tcp route must be valid: %v", err)
	}
}

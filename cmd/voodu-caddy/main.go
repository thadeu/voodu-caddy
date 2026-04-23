// Command voodu-caddy is the plugin binary exec'd by the Voodu
// controller. It reads inputs from environment variables (per the
// Voodu plugin contract) and emits a JSON envelope to stdout.
//
// Subcommands:
//
//	apply   — upsert the ingress described by $VOODU_INGRESS_*
//	remove  — delete the ingress named $VOODU_APP
//	list    — print all routes currently configured
//	reload  — re-sync the running Caddy config from disk state
//
// Contract details live in plugin.yml and in
// go.voodu.clowk.in/pkg/plugin (the Envelope type, env var names).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/thadeu/voodu-caddy/internal/caddyapi"
	"github.com/thadeu/voodu-caddy/internal/ingress"
)

// version is overridden at link time via -ldflags '-X main.version=…'
// so the install script can verify a downloaded binary matches the
// release tag (see Makefile and .github/workflows/release.yml).
var version = "dev"

// envelope mirrors pkg/plugin.Envelope from the core repo. Duplicated
// here so the plugin has no Go dependency on the core module (plugins
// are built and released independently).
type envelope struct {
	Status string `json:"status"`
	Data   any    `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Plugin env var names — duplicated from pkg/plugin for the same
// no-core-dependency reason.
const (
	envApp             = "VOODU_APP"
	envIngressHost     = "VOODU_INGRESS_HOST"
	envIngressService  = "VOODU_INGRESS_SERVICE"
	envIngressPort     = "VOODU_INGRESS_PORT"
	envIngressTLS      = "VOODU_INGRESS_TLS"
	envIngressProvider = "VOODU_INGRESS_TLS_PROVIDER"
	envIngressEmail    = "VOODU_INGRESS_TLS_EMAIL"
	envIngressOnDemand  = "VOODU_INGRESS_TLS_ON_DEMAND"
	envIngressAsk       = "VOODU_INGRESS_TLS_ASK"
	envIngressLocations = "VOODU_INGRESS_LOCATIONS"
	envIngressUpstreams = "VOODU_INGRESS_UPSTREAMS"
	envIngressLBPolicy  = "VOODU_INGRESS_LB_POLICY"
	envIngressLBInterval = "VOODU_INGRESS_LB_INTERVAL"
	envIngressHCPath    = "VOODU_INGRESS_HC_PATH"
	envCaddyAdminURL    = "VOODU_CADDY_ADMIN_URL"
	envCaddyStateDir   = "VOODU_CADDY_STATE_DIR"
	defaultStateDir    = "/opt/voodu/caddy"
)

func main() {
	if len(os.Args) < 2 {
		emit(envelope{Status: "error", Error: "usage: voodu-caddy <apply|remove|list|reload|version>"})
		os.Exit(2)
	}

	sub := os.Args[1]

	if sub == "version" || sub == "--version" || sub == "-v" {
		fmt.Println(version)
		return
	}

	env := readEnv()
	store := ingress.NewStore(env.stateDir)
	client := caddyapi.New(env.adminURL)

	var err error

	switch sub {
	case "apply":
		err = cmdApply(store, client, env)
	case "remove":
		err = cmdRemove(store, client, env)
	case "list":
		err = cmdList(store)
	case "reload":
		err = cmdReload(store, client)
	default:
		emit(envelope{Status: "error", Error: fmt.Sprintf("unknown subcommand %q", sub)})
		os.Exit(2)
	}

	if err != nil {
		emit(envelope{Status: "error", Error: err.Error()})
		os.Exit(1)
	}
}

type runEnv struct {
	app, host, service, provider, email string
	port                                int
	tls                                 bool
	onDemand                            bool
	ask                                 string
	locations                           []ingress.Location
	locationsErr                        error
	upstreams                           []string
	upstreamsErr                        error
	lbPolicy                            string
	lbInterval                          string
	hcPath                              string
	adminURL, stateDir                  string
}

func readEnv() runEnv {
	port, _ := strconv.Atoi(os.Getenv(envIngressPort))

	dir := os.Getenv(envCaddyStateDir)
	if dir == "" {
		dir = defaultStateDir
	}

	locs, locErr := parseLocations(os.Getenv(envIngressLocations))
	ups, upsErr := parseUpstreams(os.Getenv(envIngressUpstreams))

	return runEnv{
		app:          os.Getenv(envApp),
		host:         os.Getenv(envIngressHost),
		service:      os.Getenv(envIngressService),
		port:         port,
		tls:          strings.EqualFold(os.Getenv(envIngressTLS), "true"),
		provider:     os.Getenv(envIngressProvider),
		email:        os.Getenv(envIngressEmail),
		onDemand:     strings.EqualFold(os.Getenv(envIngressOnDemand), "true"),
		ask:          os.Getenv(envIngressAsk),
		locations:    locs,
		locationsErr: locErr,
		upstreams:    ups,
		upstreamsErr: upsErr,
		lbPolicy:     os.Getenv(envIngressLBPolicy),
		lbInterval:   os.Getenv(envIngressLBInterval),
		hcPath:       os.Getenv(envIngressHCPath),
		adminURL:     os.Getenv(envCaddyAdminURL),
		stateDir:     dir,
	}
}

// parseUpstreams decodes the JSON array of `host:port` strings the
// controller emits for multi-replica deployments. Empty string → nil
// (use single-upstream fallback). Accepts both the JSON array form and
// a comma-separated fallback so operators can hand-craft tests without
// the JSON noise.
func parseUpstreams(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	if strings.HasPrefix(raw, "[") {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON: %w", envIngressUpstreams, err)
		}

		return out, nil
	}

	// Comma-separated fallback for debug/manual use.
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))

	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}

	return out, nil
}

// parseLocations decodes the JSON blob the controller exports for
// path-based routing. Empty string means "no locations" (catch-all),
// which is the overwhelmingly common case — not an error.
func parseLocations(raw string) ([]ingress.Location, error) {
	if raw == "" {
		return nil, nil
	}

	var out []ingress.Location

	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", envIngressLocations, err)
	}

	return out, nil
}

// cmdApply persists the route then reloads Caddy. The two-phase flow
// matters: if the reload fails, the on-disk state already reflects the
// intent so the next `reload` (triggered by operators or the next
// apply) converges. We surface the error either way.
func cmdApply(store *ingress.Store, client *caddyapi.Client, e runEnv) error {
	if e.app == "" {
		return fmt.Errorf("%s not set (controller must export the app name)", envApp)
	}

	if e.locationsErr != nil {
		return e.locationsErr
	}

	if e.upstreamsErr != nil {
		return e.upstreamsErr
	}

	// Single-upstream fallback — used when the controller didn't export
	// VOODU_INGRESS_UPSTREAMS (older controller or single-replica app
	// where the legacy SERVICE/PORT pair is enough).
	upstream, err := ingress.UpstreamForPort(e.service, e.port)
	if err != nil {
		return err
	}

	route := ingress.Route{
		App:             e.app,
		Host:            e.host,
		Upstream:        upstream,
		Upstreams:       e.upstreams,
		LBPolicy:        e.lbPolicy,
		LBInterval:      e.lbInterval,
		HealthCheckPath: e.hcPath,
		Locations:       e.locations,
	}

	if e.tls {
		if e.provider == "" {
			return fmt.Errorf("%s=true but %s is empty", envIngressTLS, envIngressProvider)
		}

		route.TLSProvider = e.provider
		route.TLSEmail = e.email
	}

	if e.onDemand {
		// On-demand TLS without an ask URL is a footgun: Caddy would happily
		// issue certs for any hostname that resolves to this box. Refuse at
		// the plugin boundary so the operator gets a clean error.
		if e.ask == "" {
			return fmt.Errorf("%s=true requires %s (callback URL that approves hostnames)", envIngressOnDemand, envIngressAsk)
		}

		route.OnDemand = true
		route.TLSAsk = e.ask
	}

	if err := store.Put(route); err != nil {
		return err
	}

	if err := reload(store, client); err != nil {
		return err
	}

	scheme := "http"
	if e.tls {
		scheme = "https"
	}

	url := fmt.Sprintf("%s://%s", scheme, e.host)

	emit(envelope{
		Status: "ok",
		Data: map[string]any{
			"app":      e.app,
			"host":     e.host,
			"upstream": upstream,
			"url":      url,
			"tls":      e.tls,
		},
	})

	return nil
}

func cmdRemove(store *ingress.Store, client *caddyapi.Client, e runEnv) error {
	if e.app == "" {
		return fmt.Errorf("%s not set", envApp)
	}

	if err := store.Delete(e.app); err != nil {
		return err
	}

	if err := reload(store, client); err != nil {
		return err
	}

	emit(envelope{
		Status: "ok",
		Data:   map[string]any{"app": e.app, "removed": true},
	})

	return nil
}

func cmdList(store *ingress.Store) error {
	routes, err := store.List()
	if err != nil {
		return err
	}

	emit(envelope{
		Status: "ok",
		Data:   map[string]any{"routes": routes},
	})

	return nil
}

func cmdReload(store *ingress.Store, client *caddyapi.Client) error {
	if err := reload(store, client); err != nil {
		return err
	}

	emit(envelope{Status: "ok", Data: map[string]any{"reloaded": true}})

	return nil
}

// reload rebuilds the full Caddy config from on-disk routes and POSTs
// /load. It's the single convergence primitive — every subcommand that
// changes state calls this.
func reload(store *ingress.Store, client *caddyapi.Client) error {
	routes, err := store.List()
	if err != nil {
		return err
	}

	cfg := ingress.BuildCaddyConfig(routes)

	return client.Load(cfg)
}

func emit(env envelope) {
	_ = json.NewEncoder(os.Stdout).Encode(env)
}

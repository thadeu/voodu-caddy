// Package ingress owns the "ingress domain model" of the plugin — one
// declarative Ingress = one Caddy route. The package has two jobs:
//
//  1. Turn the reconciler-supplied env vars (VOODU_INGRESS_*) into a
//     Route struct.
//  2. Turn a set of Routes into a complete Caddy /load config blob.
//
// Storing individual Routes on disk (one JSON per app) lets `apply` and
// `remove` be trivial — write/delete a file, then rebuild and reload.
// Atomic replacement via POST /load means Caddy never sees a partial
// state, and reload on boot re-materialises from disk without talking
// to any other source of truth.
package ingress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Location is one URI prefix within a Route. Empty Locations means the
// Route is a catch-all for the host. Strip rewrites the request to
// remove the prefix before forwarding — useful when a generic upstream
// (static nginx) expects root-relative URIs.
type Location struct {
	Path  string `json:"path"`
	Strip bool   `json:"strip,omitempty"`
}

// Route is one host → upstream mapping. TLS is on when Provider is set.
type Route struct {
	App      string `json:"app"`
	Host     string `json:"host"`
	Upstream string `json:"upstream"`

	// Locations, when non-empty, produces one Caddy route per entry with
	// a path matcher. Empty means a single catch-all route for Host.
	Locations []Location `json:"locations,omitempty"`

	// TLSProvider is "" (HTTP-only), or "letsencrypt" / "internal".
	// Email is forwarded to Caddy's ACME issuer when Provider is an ACME
	// one.
	TLSProvider string `json:"tls_provider,omitempty"`
	TLSEmail    string `json:"tls_email,omitempty"`

	// OnDemand enables Caddy's on-demand cert issuance for this route.
	// Host can be a wildcard (e.g. "*.clowk.in") because there's no
	// up-front list of certs — each hostname hitting Caddy triggers a
	// fresh issuance gated by TLSAsk.
	OnDemand bool `json:"on_demand,omitempty"`

	// TLSAsk is the callback URL Caddy hits before issuing an on-demand
	// cert ("is this tenant hostname allowed?"). Required when OnDemand
	// is true; ignored otherwise.
	TLSAsk string `json:"tls_ask,omitempty"`
}

// Key is the stable lookup key for a route (its app name). Used as the
// filename under routes/ and as the map key when rebuilding config.
func (r Route) Key() string { return r.App }

// Validate returns an error for anything that would produce a broken
// Caddy config. We reject at the plugin boundary rather than letting
// Caddy spit out a cryptic /load error.
func (r Route) Validate() error {
	if r.App == "" {
		return fmt.Errorf("route: app is required")
	}

	if r.Host == "" {
		return fmt.Errorf("route %q: host is required", r.App)
	}

	if r.Upstream == "" {
		return fmt.Errorf("route %q: upstream is required", r.App)
	}

	return nil
}

// Store is a filesystem-backed set of Routes. State directory layout:
//
//	<root>/
//	  routes/
//	    <app>.json
//
// Every public method is safe to call when the directory does not yet
// exist — the first Put creates it.
type Store struct {
	Root string
}

// NewStore returns a Store rooted at root (typically
// /opt/voodu/caddy).
func NewStore(root string) *Store { return &Store{Root: root} }

func (s *Store) routesDir() string { return filepath.Join(s.Root, "routes") }

func (s *Store) path(app string) string {
	return filepath.Join(s.routesDir(), app+".json")
}

// Put writes or replaces the route for r.App.
func (s *Store) Put(r Route) error {
	if err := r.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(s.routesDir(), 0o755); err != nil {
		return err
	}

	blob, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}

	return atomicWrite(s.path(r.App), blob, 0o644)
}

// Delete removes the route for app. Missing is not an error — remove
// is idempotent so the reconciler can re-invoke freely.
func (s *Store) Delete(app string) error {
	err := os.Remove(s.path(app))
	if err == nil || os.IsNotExist(err) {
		return nil
	}

	return err
}

// List returns every route on disk, sorted by App for stable output
// (list command + reproducible /load blobs).
func (s *Store) List() ([]Route, error) {
	entries, err := os.ReadDir(s.routesDir())
	if os.IsNotExist(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var out []Route

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(s.routesDir(), e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}

		var r Route

		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}

		out = append(out, r)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].App < out[j].App })

	return out, nil
}

// atomicWrite writes to a sibling temp file and renames. Two concurrent
// Puts for the same app can't produce a half-written JSON that List
// then fails to parse.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

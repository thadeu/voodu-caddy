// Package caddyapi is a tiny client for the Caddy Admin API.
//
// The Admin API is documented at https://caddyserver.com/docs/api. We
// only use a handful of endpoints:
//
//   POST /load           — replace entire config atomically
//   GET  /config/        — read current config
//   GET  /reverse_proxy/upstreams — sanity check during `list`
//
// The client is intentionally thin: it does not model the Caddy JSON
// schema. The plugin constructs config blobs as map[string]any and lets
// the client round-trip them — that way we never drift out of sync with
// upstream Caddy when schemas evolve.
package caddyapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a Caddy Admin API endpoint.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// DefaultAdminURL is where Caddy listens by default.
const DefaultAdminURL = "http://127.0.0.1:2019"

// New constructs a client with a sensible timeout. BaseURL defaults to
// DefaultAdminURL when empty so callers can pass a zero value for local
// usage.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultAdminURL
	}

	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Load atomically replaces the running config. This is the simplest
// reconciliation primitive Caddy offers: build the desired state,
// POST /load, done. No merge, no patch, no stale references.
func (c *Client) Load(cfg any) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("caddy: marshal config: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/load", bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	return c.do(req, nil)
}

// GetConfig reads the current running config. Used by `list` and
// internal diffing.
func (c *Client) GetConfig() (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/config/", nil)
	if err != nil {
		return nil, err
	}

	var out map[string]any
	if err := c.do(req, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// do executes req, surfaces non-2xx as errors carrying Caddy's own error
// body, and JSON-decodes into dst when non-nil.
func (c *Client) do(req *http.Request, dst any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("caddy: %s %s: %w", req.Method, req.URL.Path, err)
	}

	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("caddy: %s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if dst == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("caddy: decode %s: %w", req.URL.Path, err)
	}

	return nil
}

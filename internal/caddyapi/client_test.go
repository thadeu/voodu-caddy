package caddyapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoad_PostsJSONToSlashLoad(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotType   string
		gotBody   []byte
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := New(ts.URL)

	cfg := map[string]any{"apps": map[string]any{"http": map[string]any{}}}
	if err := c.Load(cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/load" {
		t.Errorf("unexpected request: %s %s", gotMethod, gotPath)
	}

	if !strings.HasPrefix(gotType, "application/json") {
		t.Errorf("Content-Type = %q", gotType)
	}

	var roundTrip map[string]any

	if err := json.Unmarshal(gotBody, &roundTrip); err != nil {
		t.Fatalf("body not JSON: %v\nraw: %s", err, gotBody)
	}

	if _, ok := roundTrip["apps"]; !ok {
		t.Errorf("body missing 'apps' key: %v", roundTrip)
	}
}

func TestLoad_SurfacesErrorBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"adapting config using JSON: unexpected token"}`, http.StatusBadRequest)
	}))
	defer ts.Close()

	err := New(ts.URL).Load(map[string]any{})
	if err == nil {
		t.Fatal("expected error")
	}

	// The caller needs Caddy's own message surfaced, not a generic HTTP
	// error — that's how plugin authors debug bad configs.
	if !strings.Contains(err.Error(), "unexpected token") {
		t.Errorf("error does not include caddy body: %v", err)
	}
}

func TestGetConfig_ReturnsParsedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			http.NotFound(w, r)
			return
		}

		_, _ = w.Write([]byte(`{"apps":{"http":{"servers":{}}}}`))
	}))
	defer ts.Close()

	got, err := New(ts.URL).GetConfig()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := got["apps"]; !ok {
		t.Errorf("missing apps: %+v", got)
	}
}

func TestNew_DefaultURLWhenEmpty(t *testing.T) {
	if got := New("").BaseURL; got != DefaultAdminURL {
		t.Errorf("empty BaseURL default = %q, want %q", got, DefaultAdminURL)
	}

	if got := New("http://host:2020/").BaseURL; got != "http://host:2020" {
		t.Errorf("trailing slash not stripped: %q", got)
	}
}

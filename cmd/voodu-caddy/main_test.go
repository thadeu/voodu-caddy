package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPluginEndToEnd spawns the real voodu-caddy binary against a mock
// Caddy Admin API and walks the full apply → list → remove cycle. This
// is the test that catches env-var renames, envelope shape drift, and
// reload ordering bugs — anything higher-level fakes would miss.
func TestPluginEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin targets linux/darwin")
	}

	var (
		loads []map[string]any
	)

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/load" {
			raw, _ := io.ReadAll(r.Body)

			var cfg map[string]any
			_ = json.Unmarshal(raw, &cfg)
			loads = append(loads, cfg)

			w.WriteHeader(http.StatusOK)

			return
		}

		http.NotFound(w, r)
	}))
	defer admin.Close()

	stateDir := t.TempDir()

	bin := buildPluginBinary(t)

	runPlugin := func(sub string, env map[string]string) map[string]any {
		t.Helper()

		full := map[string]string{
			"VOODU_CADDY_ADMIN_URL": admin.URL,
			"VOODU_CADDY_STATE_DIR": stateDir,
		}

		for k, v := range env {
			full[k] = v
		}

		cmd := exec.Command(bin, sub)
		cmd.Env = envSlice(full)

		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout

		if err := cmd.Run(); err != nil {
			t.Fatalf("plugin %s: %v\nstdout: %s", sub, err, stdout.String())
		}

		var env2 map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env2); err != nil {
			t.Fatalf("plugin %s stdout not JSON: %v\nraw: %s", sub, err, stdout.String())
		}

		return env2
	}

	applyResp := runPlugin("apply", map[string]string{
		"VOODU_APP":                  "api",
		"VOODU_INGRESS_HOST":         "api.example.com",
		"VOODU_INGRESS_SERVICE":      "api",
		"VOODU_INGRESS_PORT":         "3000",
		"VOODU_INGRESS_TLS":          "true",
		"VOODU_INGRESS_TLS_PROVIDER": "letsencrypt",
		"VOODU_INGRESS_TLS_EMAIL":    "ops@example.com",
	})

	if applyResp["status"] != "ok" {
		t.Fatalf("apply status: %+v", applyResp)
	}

	data := applyResp["data"].(map[string]any)
	if data["url"] != "https://api.example.com" {
		t.Errorf("url: %v", data["url"])
	}

	if data["upstream"] != "api:3000" {
		t.Errorf("upstream: %v", data["upstream"])
	}

	// Route should be on disk.
	if _, err := os.Stat(filepath.Join(stateDir, "routes", "api.json")); err != nil {
		t.Errorf("route file not persisted: %v", err)
	}

	// A single /load happened carrying the api.example.com route.
	if len(loads) != 1 {
		t.Fatalf("expected 1 /load, got %d", len(loads))
	}

	if !strings.Contains(marshalJSON(t, loads[0]), "api.example.com") {
		t.Errorf("/load blob missing host: %s", marshalJSON(t, loads[0]))
	}

	// list should see the route.
	listResp := runPlugin("list", nil)

	listData := listResp["data"].(map[string]any)["routes"].([]any)
	if len(listData) != 1 {
		t.Errorf("list returned %d routes", len(listData))
	}

	// remove converges to empty config.
	removeResp := runPlugin("remove", map[string]string{"VOODU_APP": "api"})

	if removeResp["status"] != "ok" {
		t.Errorf("remove failed: %+v", removeResp)
	}

	if _, err := os.Stat(filepath.Join(stateDir, "routes", "api.json")); !os.IsNotExist(err) {
		t.Errorf("route file still on disk: %v", err)
	}

	// Two /load calls total — apply and remove.
	if len(loads) != 2 {
		t.Errorf("expected 2 /load total, got %d", len(loads))
	}

	// Remove is idempotent — running it again shouldn't error.
	second := runPlugin("remove", map[string]string{"VOODU_APP": "api"})
	if second["status"] != "ok" {
		t.Errorf("idempotent remove: %+v", second)
	}
}

func TestPluginApply_LocationsJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin targets linux/darwin")
	}

	var loads []map[string]any

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/load" {
			raw, _ := io.ReadAll(r.Body)

			var cfg map[string]any
			_ = json.Unmarshal(raw, &cfg)
			loads = append(loads, cfg)

			w.WriteHeader(http.StatusOK)

			return
		}

		http.NotFound(w, r)
	}))
	defer admin.Close()

	stateDir := t.TempDir()
	bin := buildPluginBinary(t)

	cmd := exec.Command(bin, "apply")
	cmd.Env = envSlice(map[string]string{
		"VOODU_CADDY_ADMIN_URL":    admin.URL,
		"VOODU_CADDY_STATE_DIR":    stateDir,
		"VOODU_APP":                "docs",
		"VOODU_INGRESS_HOST":       "example.com",
		"VOODU_INGRESS_SERVICE":    "docs",
		"VOODU_INGRESS_PORT":       "80",
		"VOODU_INGRESS_LOCATIONS":  `[{"path":"/docs/voodu","strip":true},{"path":"/api/v1"}]`,
	})

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("plugin apply: %v\n%s", err, stdout.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("apply stdout not JSON: %v\nraw: %s", err, stdout.String())
	}

	if resp["status"] != "ok" {
		t.Fatalf("apply status: %+v", resp)
	}

	if len(loads) != 1 {
		t.Fatalf("expected 1 /load, got %d", len(loads))
	}

	blob := marshalJSON(t, loads[0])

	// Two path matchers (docs strip + api non-strip), one rewrite.
	if !strings.Contains(blob, `"path":["/docs/voodu","/docs/voodu/*"]`) {
		t.Errorf("strip location path missing: %s", blob)
	}

	if !strings.Contains(blob, `"path":["/api/v1","/api/v1/*"]`) {
		t.Errorf("non-strip location path missing: %s", blob)
	}

	if !strings.Contains(blob, `"strip_path_prefix":"/docs/voodu"`) {
		t.Errorf("rewrite handler missing: %s", blob)
	}
}

func TestPluginApply_LocationsInvalidJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin targets linux/darwin")
	}

	bin := buildPluginBinary(t)

	cmd := exec.Command(bin, "apply")
	cmd.Env = envSlice(map[string]string{
		"VOODU_CADDY_ADMIN_URL":    "http://127.0.0.1:0",
		"VOODU_CADDY_STATE_DIR":    t.TempDir(),
		"VOODU_APP":                "api",
		"VOODU_INGRESS_HOST":       "api.example.com",
		"VOODU_INGRESS_SERVICE":    "api",
		"VOODU_INGRESS_PORT":       "3000",
		"VOODU_INGRESS_LOCATIONS":  `not-json`,
	})

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}

	var resp map[string]any
	if jsonErr := json.Unmarshal(stdout.Bytes(), &resp); jsonErr != nil {
		t.Fatalf("stdout not JSON: %v\nraw: %s", jsonErr, stdout.String())
	}

	if resp["status"] != "error" {
		t.Errorf("status: %v", resp["status"])
	}

	if !strings.Contains(resp["error"].(string), "VOODU_INGRESS_LOCATIONS") {
		t.Errorf("error should mention VOODU_INGRESS_LOCATIONS: %v", resp["error"])
	}
}

func TestPluginApply_MissingRequiredEnv(t *testing.T) {
	bin := buildPluginBinary(t)

	cmd := exec.Command(bin, "apply")
	cmd.Env = envSlice(map[string]string{
		"VOODU_CADDY_ADMIN_URL": "http://127.0.0.1:0", // unreachable; we fail earlier anyway
		"VOODU_CADDY_STATE_DIR": t.TempDir(),
	})

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}

	var env map[string]any
	if jsonErr := json.Unmarshal(stdout.Bytes(), &env); jsonErr != nil {
		t.Fatalf("stdout not JSON: %v\nraw: %s", jsonErr, stdout.String())
	}

	if env["status"] != "error" {
		t.Errorf("status: %v", env["status"])
	}

	if !strings.Contains(env["error"].(string), "VOODU_APP") {
		t.Errorf("error should mention missing VOODU_APP: %v", env["error"])
	}
}

func buildPluginBinary(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "voodu-caddy")

	cmd := exec.Command("go", "build", "-o", bin, ".")

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out.String())
	}

	return bin
}

func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m)+2)

	// PATH so `go build` during test subprocess works; HOME so go tooling
	// can find its cache. Anything else is intentionally unset so we
	// don't accidentally leak developer env into the plugin.
	if path := os.Getenv("PATH"); path != "" {
		out = append(out, "PATH="+path)
	}

	if home := os.Getenv("HOME"); home != "" {
		out = append(out, "HOME="+home)
	}

	for k, v := range m {
		out = append(out, k+"="+v)
	}

	return out
}

func marshalJSON(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

package ingress

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRouteValidate(t *testing.T) {
	cases := []struct {
		name string
		in   Route
		ok   bool
	}{
		{"complete", Route{App: "api", Host: "api.x.com", Upstream: "api:3000"}, true},
		{"missing app", Route{Host: "api.x.com", Upstream: "api:3000"}, false},
		{"missing host", Route{App: "api", Upstream: "api:3000"}, false},
		{"missing upstream", Route{App: "api", Host: "api.x.com"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.in.Validate()
			if (err == nil) != c.ok {
				t.Errorf("Validate ok=%v, err=%v", c.ok, err)
			}
		})
	}
}

func TestStorePutListDelete(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// List on empty store returns nil/empty, not an error — reload has to
	// work on a fresh install.
	got, err := s.List()
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected no routes, got %d", len(got))
	}

	r1 := Route{App: "api", Host: "api.x.com", Upstream: "api:3000", TLSProvider: "letsencrypt", TLSEmail: "ops@x.com"}
	r2 := Route{App: "web", Host: "x.com", Upstream: "web:8080"}

	if err := s.Put(r1); err != nil {
		t.Fatal(err)
	}

	if err := s.Put(r2); err != nil {
		t.Fatal(err)
	}

	got, err = s.List()
	if err != nil {
		t.Fatal(err)
	}

	// Sorted by App: api, web.
	want := []Route{r1, r2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List mismatch:\n got  %+v\n want %+v", got, want)
	}

	if _, err := os.Stat(filepath.Join(root, "routes", "api.json")); err != nil {
		t.Errorf("route file not on disk: %v", err)
	}

	if err := s.Delete("api"); err != nil {
		t.Fatal(err)
	}

	// Delete is idempotent.
	if err := s.Delete("api"); err != nil {
		t.Errorf("second delete should be a no-op: %v", err)
	}

	got, _ = s.List()
	if len(got) != 1 || got[0].App != "web" {
		t.Errorf("after delete, got: %+v", got)
	}
}

func TestStorePut_RejectsInvalid(t *testing.T) {
	s := NewStore(t.TempDir())

	err := s.Put(Route{App: "api"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}


package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type versionsPayload struct {
	Env      string `json:"env"`
	Count    int    `json:"count"`
	Error    *string
	Services []struct {
		Name         string `json:"name"`
		AppVersion   string `json:"appVersion"`
		ChartVersion string `json:"chartVersion"`
		Source       string `json:"source"`
		State        string `json:"state"`
	} `json:"services"`
	Filters *struct {
		Status  []string `json:"status"`
		Matched int      `json:"matched"`
	} `json:"filters"`
}

func getVersions(t *testing.T, h http.Handler, path string) versionsPayload {
	t.Helper()
	rec := get(t, h, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var p versionsPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return p
}

func TestVersionsEndpoint(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status", EnvName: "sample-dev"},
		fakeProvider{snap: fixtureSnapshot(t)})

	p := getVersions(t, h, "/k8s-status/api/versions")
	if p.Env != "sample-dev" {
		t.Errorf("env = %q", p.Env)
	}
	if p.Count == 0 || p.Count != len(p.Services) {
		t.Fatalf("count = %d, services = %d", p.Count, len(p.Services))
	}
	var withApp int
	for _, s := range p.Services {
		if s.Name == "" {
			t.Error("every row needs a name")
		}
		if s.AppVersion != "" {
			withApp++
		}
	}
	if withApp == 0 {
		t.Error("want at least one appVersion; that is the point of the endpoint")
	}
}

func TestVersionsHonoursFilters(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	all := getVersions(t, h, "/k8s-status/api/versions")
	deg := getVersions(t, h, "/k8s-status/api/versions?status=DEGRADED")

	if deg.Count >= all.Count {
		t.Errorf("filtered count %d should be smaller than %d", deg.Count, all.Count)
	}
	for _, s := range deg.Services {
		if s.State != "DEGRADED" {
			t.Errorf("%s has state %q under ?status=DEGRADED", s.Name, s.State)
		}
	}
	if deg.Filters == nil || deg.Filters.Matched != deg.Count {
		t.Error("the response should echo the filter it applied")
	}
	if all.Filters != nil {
		t.Error("no filter means no filters block")
	}
}

func TestVersionsStillAnswersWhenClusterReadFails(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"},
		fakeProvider{err: errors.New("connection refused")})

	p := getVersions(t, h, "/k8s-status/api/versions")
	if p.Error == nil {
		t.Error("want the error reported in the payload, not a 500")
	}
	if p.Count != 0 || len(p.Services) != 0 {
		t.Error("want an empty service list on failure")
	}
}

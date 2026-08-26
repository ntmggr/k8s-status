package status

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ntmggr/k8s-status/internal/argocd"
)

const fixturePath = "../../testdata/applications.json"

func loadFixture(t *testing.T) *argocd.ApplicationList {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var list argocd.ApplicationList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return &list
}

func app(name, health, sync string) argocd.Application {
	var a argocd.Application
	a.Metadata.Name = name
	a.Status.Health.Status = health
	a.Status.Sync.Status = sync
	return a
}

func rootApp(phase string, pruning ...string) argocd.Application {
	var a argocd.Application
	a.Metadata.Name = "root-app"
	a.Status.OperationState.Phase = phase
	for _, name := range pruning {
		a.Status.Resources = append(a.Status.Resources, argocd.Resource{Name: name, RequiresPruning: true})
	}
	return a
}

func TestClassifyRules(t *testing.T) {
	cases := []struct {
		name      string
		rootPhase string
		pruning   []string
		child     argocd.Application
		want      State
	}{
		{"prune flag wins", "Succeeded", []string{"cache-shim"}, app("cache-shim", "Healthy", "Synced"), StatePrune},
		{"prune beats degraded", "Succeeded", []string{"report-runner"}, app("report-runner", "Degraded", "OutOfSync"), StatePrune},
		{"health degraded", "Succeeded", nil, app("media-encoder", "Degraded", "Synced"), StateDegraded},
		{"health missing stays degraded", "Succeeded", nil, app("orders-api", "Missing", "Synced"), StateDegraded},
		{"health unknown is a warning, not degraded", "Succeeded", nil, app("search-api", "Unknown", "Synced"), StateWarning},
		{"empty health normalizes to unknown and warns", "Succeeded", nil, app("search-api", "", "Synced"), StateWarning},
		{"health progressing", "Succeeded", nil, app("ingest-gateway", "Progressing", "Synced"), StateProgressing},
		{"out of sync while root running", "Running", nil, app("edge-proxy", "Healthy", "OutOfSync"), StateProgressing},
		{"health suspended", "Succeeded", nil, app("metrics-collector", "Suspended", "Synced"), StateSuspended},
		{"healthy but out of sync once root settled is drift, not degraded", "Succeeded", nil, app("edge-proxy", "Healthy", "OutOfSync"), StateDrift},
		{"unhealthy and out of sync stays degraded", "Succeeded", nil, app("edge-proxy", "Degraded", "OutOfSync"), StateDegraded},
		{"healthy and synced", "Succeeded", nil, app("accounts-api", "Healthy", "Synced"), StateOK},
		{"suspended still prunes first", "Succeeded", []string{"metrics-collector"}, app("metrics-collector", "Suspended", "Synced"), StatePrune},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := &argocd.ApplicationList{Items: []argocd.Application{rootApp(tc.rootPhase, tc.pruning...), tc.child}}
			snap := Build(list, Options{RootAppName: "root-app"})
			if len(snap.Services) != 1 {
				t.Fatalf("want 1 service, got %d", len(snap.Services))
			}
			if snap.Services[0].State != tc.want {
				t.Errorf("state = %s, want %s", snap.Services[0].State, tc.want)
			}
		})
	}
}

func TestEmptyHealthNormalized(t *testing.T) {
	list := &argocd.ApplicationList{Items: []argocd.Application{rootApp("Succeeded"), app("search-api", "", "Synced")}}
	snap := Build(list, Options{RootAppName: "root-app"})
	if got := snap.Services[0].Health; got != "Unknown" {
		t.Errorf("health = %q, want %q", got, "Unknown")
	}
}

func TestBuildFixtureSummary(t *testing.T) {
	snap := Build(loadFixture(t), Options{RootAppName: "root-app"})

	want := Summary{Total: 14, OK: 6, Degraded: 2, Warning: 1, Progressing: 1, Drift: 1, Prune: 2, Suspended: 1, Hidden: 0}
	if snap.Summary != want {
		t.Errorf("summary = %+v, want %+v", snap.Summary, want)
	}
	if snap.EnvType != "dev" {
		t.Errorf("envType = %q, want dev", snap.EnvType)
	}
	if snap.Version != "develop" {
		t.Errorf("version = %q, want develop", snap.Version)
	}
	if snap.LastDeployID != 1048 || snap.LastDeployedAt != "2026-08-21T11:30:57Z" {
		t.Errorf("last deploy = %d/%s", snap.LastDeployID, snap.LastDeployedAt)
	}
	if snap.RootHealth != "Degraded" || snap.RootSync != "OutOfSync" || snap.Phase != "Succeeded" {
		t.Errorf("root state = %s/%s/%s", snap.RootHealth, snap.RootSync, snap.Phase)
	}
}

func TestNullRevisionDecodesToEmpty(t *testing.T) {
	snap := Build(loadFixture(t), Options{RootAppName: "root-app"})
	for _, svc := range snap.Services {
		if svc.Name == "session-store" {
			if svc.Revision != "" {
				t.Errorf("revision = %q, want empty for null revision", svc.Revision)
			}
			if svc.State != StateOK {
				t.Errorf("state = %s, want OK", svc.State)
			}
			return
		}
	}
	t.Fatal("session-store not found in fixture")
}

func TestSortOrderIsDeterministic(t *testing.T) {
	want := []string{
		"media-encoder", "orders-api",
		"search-api",
		"ingest-gateway",
		"edge-proxy",
		"cache-shim", "report-runner",
		"metrics-collector",
		"accounts-api", "admin-ui", "log-shipper", "notify-worker", "session-store", "toolbox",
	}

	for i := 0; i < 20; i++ {
		snap := Build(loadFixture(t), Options{RootAppName: "root-app"})
		got := make([]string, 0, len(snap.Services))
		for _, svc := range snap.Services {
			got = append(got, svc.Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d order = %v, want %v", i, got, want)
		}
	}
}

func TestIgnoreGlobsHideAndCount(t *testing.T) {
	snap := Build(loadFixture(t), Options{
		RootAppName: "root-app",
		IgnoreGlobs: []string{"toolbox*", " report-* "},
	})

	if snap.Summary.Hidden != 2 {
		t.Errorf("hidden = %d, want 2", snap.Summary.Hidden)
	}
	if snap.Summary.Total != 12 {
		t.Errorf("total = %d, want 12", snap.Summary.Total)
	}
	if snap.Summary.Prune != 1 {
		t.Errorf("prune = %d, want 1 (report-runner hidden)", snap.Summary.Prune)
	}
	for _, svc := range snap.Services {
		if svc.Name == "toolbox" || svc.Name == "report-runner" {
			t.Errorf("%s should be hidden", svc.Name)
		}
	}
}

func TestDetailTruncatedTo200(t *testing.T) {
	long := make([]byte, 320)
	for i := range long {
		long[i] = 'x'
	}
	child := app("accounts-api", "Degraded", "Synced")
	child.Status.Health.Message = string(long)

	snap := Build(&argocd.ApplicationList{Items: []argocd.Application{rootApp("Succeeded"), child}}, Options{RootAppName: "root-app"})
	if got := len(snap.Services[0].Detail); got != 200 {
		t.Errorf("detail length = %d, want 200", got)
	}
}

func TestBuildWithoutRootTreatsRootAsSettled(t *testing.T) {
	list := &argocd.ApplicationList{Items: []argocd.Application{app("edge-proxy", "Healthy", "OutOfSync")}}
	snap := Build(list, Options{RootAppName: "root-app"})
	if snap.Services[0].State != StateDrift {
		t.Errorf("state = %s, want DEGRADED", snap.Services[0].State)
	}
}

// The app-of-apps is found by shape, so the tool works on clusters that call it
// something other than whatever this repo happened to be written against.
func TestDetectRootAppByShape(t *testing.T) {
	app := func(name string, kids int) argocd.Application {
		var a argocd.Application
		a.Metadata.Name = name
		for i := 0; i < kids; i++ {
			a.Status.Resources = append(a.Status.Resources,
				argocd.Resource{Group: "argoproj.io", Kind: "Application", Name: "child"})
		}
		return a
	}
	items := []argocd.Application{
		app("orders-api", 0),
		app("platform-root", 12),
		app("nested-root", 3),
	}
	if got := detectRootApp(items); got != "platform-root" {
		t.Errorf("detectRootApp = %q, want platform-root (most Application children)", got)
	}
	// A workload that owns ConfigMaps, not Applications, is not a root.
	var plain argocd.Application
	plain.Metadata.Name = "orders-api"
	plain.Status.Resources = []argocd.Resource{{Group: "", Kind: "ConfigMap", Name: "cm"}}
	if got := detectRootApp([]argocd.Application{plain}); got != "" {
		t.Errorf("detectRootApp = %q, want empty when nothing owns Applications", got)
	}
}

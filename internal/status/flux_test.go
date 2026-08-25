package status

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// loadFluxFixture reads the two Flux fixtures. They are sanitised copies of what a real
// Flux install answered with, so the shapes here are observed rather than assumed.
func loadFluxFixture(t *testing.T) *kube.FluxList {
	t.Helper()
	var out kube.FluxList

	var hrs kube.HelmReleaseList
	readJSON(t, "../../testdata/helmreleases.json", &hrs)
	out.HelmReleases = hrs.Items

	var ks kube.KustomizationList
	readJSON(t, "../../testdata/kustomizations.json", &ks)
	out.Kustomizations = ks.Items

	return &out
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func cond(kind, status, reason, message string) kube.Condition {
	return kube.Condition{Type: kind, Status: status, Reason: reason, Message: message}
}

func TestFluxVerdictMapsConditionsToStates(t *testing.T) {
	tests := []struct {
		name    string
		suspend bool
		conds   []kube.Condition
		want    State
		health  string
		detail  string
	}{
		{
			name:   "ready true is OK",
			conds:  []kube.Condition{cond("Ready", "True", "ReconciliationSucceeded", "Applied revision: main@sha1:abc")},
			want:   StateOK,
			health: healthHealthy,
		},
		{
			name:   "ready false is DEGRADED",
			conds:  []kube.Condition{cond("Ready", "False", "ArtifactFailed", `GitRepository "x" not found`)},
			want:   StateDegraded,
			health: healthDegraded,
			detail: `ArtifactFailed: GitRepository "x" not found`,
		},
		{
			name:   "missing ready is WARNING",
			conds:  nil,
			want:   StateWarning,
			health: healthUnknown,
			detail: "no Ready condition reported yet",
		},
		{
			// kustomize-controller sets Ready=Unknown while it reconciles, so this is
			// the shape a mid-flight object really has.
			name:   "ready unknown with reconciling is PROGRESSING",
			conds:  []kube.Condition{cond("Reconciling", "True", "Progressing", "Fetching manifests"), cond("Ready", "Unknown", "Progressing", "Reconciliation in progress")},
			want:   StateProgressing,
			health: healthProgressing,
			detail: "Progressing: Fetching manifests",
		},
		{
			name:   "ready unknown alone is WARNING",
			conds:  []kube.Condition{cond("Ready", "Unknown", "Progressing", "Reconciliation in progress")},
			want:   StateWarning,
			health: healthUnknown,
			detail: "Progressing: Reconciliation in progress",
		},
		{
			// helm-controller keeps Reconciling=True next to a failed Ready. The
			// failure is the news, so it must win.
			name:   "ready false outranks reconciling",
			conds:  []kube.Condition{cond("Reconciling", "True", "Progressing", "Fulfilling prerequisites"), cond("Ready", "False", "SourceNotReady", "chart is not ready")},
			want:   StateDegraded,
			health: healthDegraded,
			detail: "SourceNotReady: chart is not ready",
		},
		{
			// Stalled is how Flux says it has given up retrying, and it is always
			// reported alongside Ready=False.
			name:   "stalled with ready false is DEGRADED and says so",
			conds:  []kube.Condition{cond("Stalled", "True", "RetriesExceeded", "Failed to install after 1 attempt(s)"), cond("Ready", "False", "InstallFailed", "helm install failed")},
			want:   StateDegraded,
			health: healthDegraded,
			detail: "stalled, no further retries: InstallFailed: helm install failed",
		},
		{
			name:   "stalled alone is WARNING",
			conds:  []kube.Condition{cond("Stalled", "True", "RetriesExceeded", "Failed to install after 1 attempt(s)")},
			want:   StateWarning,
			health: healthUnknown,
			detail: "stalled, no further retries: RetriesExceeded: Failed to install after 1 attempt(s)",
		},
		{
			// A suspended object keeps the conditions it had when it was paused, so
			// suspend has to win outright or stale news is reported as current.
			name:    "suspend wins over a stale ready false",
			suspend: true,
			conds:   []kube.Condition{cond("Ready", "False", "InstallFailed", "helm install failed")},
			want:    StateSuspended,
			health:  healthSuspended,
			detail:  "reconciliation suspended (spec.suspend)",
		},
		{
			name:    "suspend with no conditions at all",
			suspend: true,
			want:    StateSuspended,
			health:  healthSuspended,
			detail:  "reconciliation suspended (spec.suspend)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, health, detail := fluxVerdict(tc.suspend, tc.conds)
			if st != tc.want {
				t.Errorf("state = %s, want %s", st, tc.want)
			}
			if health != tc.health {
				t.Errorf("health = %q, want %q", health, tc.health)
			}
			if detail != tc.detail {
				t.Errorf("detail = %q, want %q", detail, tc.detail)
			}
		})
	}
}

func TestParseFluxRevision(t *testing.T) {
	tests := []struct{ in, ref, sha string }{
		{"master@sha1:eec06d1ea459af4cb4e10e806f8be7c7bd58b361", "master", "eec06d1ea459af4cb4e10e806f8be7c7bd58b361"},
		{"main@sha1:abc123def", "main", "abc123def"},
		{"refs/heads/main@sha1:abc123def", "refs/heads/main", "abc123def"},
		{"latest@sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0", "latest", "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"},
		// The pre-v1 form.
		{"main/abc123def4567", "main", "abc123def4567"},
		// A bare digest must not be mistaken for a branch name.
		{"sha1:abc123def", "", "abc123def"},
		{"abc123def4567", "", "abc123def4567"},
		// A tag or branch on its own.
		{"v1.2.3", "v1.2.3", ""},
		{"main", "main", ""},
		{"release/2026-08", "release/2026-08", ""},
		{"  main@sha1:abc123def  ", "main", "abc123def"},
		{"", "", ""},
	}
	for _, tc := range tests {
		ref, sha := parseFluxRevision(tc.in)
		if ref != tc.ref || sha != tc.sha {
			t.Errorf("parseFluxRevision(%q) = (%q, %q), want (%q, %q)", tc.in, ref, sha, tc.ref, tc.sha)
		}
	}
}

func TestBuildFluxServicesFromFixtures(t *testing.T) {
	svcs := BuildFluxServices(loadFluxFixture(t), Options{GPUGlobs: []string{"media-*"}})

	byName := map[string]Service{}
	for _, s := range svcs {
		if s.Source != SourceFlux {
			t.Errorf("%s: source = %q, want flux", s.Name, s.Source)
		}
		// Flux cannot report either of these, so no row may claim them.
		if s.State == StateDrift || s.State == StatePrune {
			t.Errorf("%s: state = %s, which has no Flux equivalent", s.Name, s.State)
		}
		if s.Sync != "" {
			t.Errorf("%s: sync = %q, want empty", s.Name, s.Sync)
		}
		if s.Namespace != "sample-apps" {
			t.Errorf("%s: namespace = %q", s.Name, s.Namespace)
		}
		byName[s.Name] = s
	}

	if len(svcs) != 9 {
		t.Fatalf("services = %d, want 9", len(svcs))
	}

	want := map[string]State{
		"orders-api":      StateOK,
		"billing-worker":  StateDegraded,
		"legacy-exporter": StateSuspended,
		"media-encoder":   StateDegraded,
		"search-api":      StateWarning,
		"platform-config": StateOK,
		"edge-proxy":      StateProgressing,
		"tenant-config":   StateDegraded,
		"archive-sync":    StateSuspended,
	}
	for name, state := range want {
		got, ok := byName[name]
		if !ok {
			t.Errorf("%s missing from the table", name)
			continue
		}
		if got.State != state {
			t.Errorf("%s: state = %s, want %s", name, got.State, state)
		}
	}

	// HelmRelease: the chart version comes from the spec, the app version from the
	// live release in history, and there is no git revision to show.
	hr := byName["orders-api"]
	if hr.Kind != kube.KindHelmRelease {
		t.Errorf("kind = %q", hr.Kind)
	}
	if hr.Version != "6.5.4" {
		t.Errorf("chart version = %q, want the spec version", hr.Version)
	}
	if hr.AppVersion != "6.5.4" {
		t.Errorf("app version = %q", hr.AppVersion)
	}
	if hr.Revision != "" {
		t.Errorf("revision = %q, want empty: a HelmRelease has no git revision", hr.Revision)
	}

	// Kustomization: the revision splits into a branch and a commit.
	ks := byName["platform-config"]
	if ks.Kind != kube.KindKustomization {
		t.Errorf("kind = %q", ks.Kind)
	}
	if ks.Version != "master" || ks.Revision != "eec06d1ea459af4cb4e10e806f8be7c7bd58b361" {
		t.Errorf("version/revision = %q/%q", ks.Version, ks.Revision)
	}

	if !byName["media-encoder"].GPU {
		t.Error("GPU globs should apply to Flux rows too")
	}
	if byName["orders-api"].GPU {
		t.Error("orders-api should not be marked GPU")
	}
	if !strings.Contains(byName["media-encoder"].Detail, "stalled") {
		t.Errorf("stalled release detail = %q", byName["media-encoder"].Detail)
	}
}

func TestAppendFluxJoinsTheSummaryAndOrdering(t *testing.T) {
	snap := Build(loadFixture(t), Options{RootAppName: "root-app"})
	argoTotal := snap.Summary.Total
	argoDrift, argoPrune := snap.Summary.Drift, snap.Summary.Prune

	snap.AppendFlux(loadFluxFixture(t), nil, Options{})

	if got, want := snap.Summary.Total, argoTotal+9; got != want {
		t.Errorf("total = %d, want %d", got, want)
	}
	// Flux rows can never reach either of these states, so both counts must be untouched.
	if snap.Summary.Drift != argoDrift || snap.Summary.Prune != argoPrune {
		t.Errorf("drift/prune = %d/%d, want %d/%d", snap.Summary.Drift, snap.Summary.Prune, argoDrift, argoPrune)
	}

	sum := snap.Summary
	if got := sum.OK + sum.Degraded + sum.Warning + sum.Progressing + sum.Drift + sum.Prune + sum.Suspended; got != sum.Total {
		t.Errorf("tiles sum to %d but total is %d", got, sum.Total)
	}
	if len(snap.Services) != sum.Total {
		t.Errorf("rows = %d, total = %d", len(snap.Services), sum.Total)
	}

	// Rows from both sources are interleaved worst-first, not appended in blocks.
	for i := 1; i < len(snap.Services); i++ {
		a, b := snap.Services[i-1], snap.Services[i]
		if severity[a.State] > severity[b.State] {
			t.Fatalf("rows out of order at %d: %s then %s", i, a.State, b.State)
		}
	}

	if snap.Flux == nil || snap.Flux.HelmReleases != 5 || snap.Flux.Kustomizations != 4 {
		t.Errorf("flux section = %+v", snap.Flux)
	}
}

func TestAppendFluxHonoursIgnoreGlobs(t *testing.T) {
	var snap Snapshot
	snap.AppendFlux(loadFluxFixture(t), nil, Options{IgnoreGlobs: []string{"tenant-*", "archive-sync"}})

	if snap.Summary.Hidden != 2 {
		t.Errorf("hidden = %d, want 2", snap.Summary.Hidden)
	}
	for _, s := range snap.Services {
		if s.Name == "tenant-config" || s.Name == "archive-sync" {
			t.Errorf("%s should have been hidden", s.Name)
		}
	}
}

func TestAppendFluxClassifiesReadFailures(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		denied, absent bool
	}{
		{"denied", &kube.StatusError{Code: 403, Body: "forbidden"}, true, false},
		{"unauthorized", &kube.StatusError{Code: 401, Body: "unauthorized"}, true, false},
		{"crds absent", &kube.StatusError{Code: 404, Body: "404 page not found"}, false, true},
		{"outage", errors.New("connection refused"), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(loadFixture(t), Options{RootAppName: "root-app"})
			before := snap.Summary.Total

			snap.AppendFlux(nil, tc.err, Options{})

			if snap.Flux == nil {
				t.Fatal("want a flux section carrying the note")
			}
			if snap.Flux.Error == "" {
				t.Error("want the error recorded")
			}
			if snap.Flux.Denied != tc.denied || snap.Flux.Missing != tc.absent {
				t.Errorf("denied/missing = %t/%t, want %t/%t", snap.Flux.Denied, snap.Flux.Missing, tc.denied, tc.absent)
			}
			// The ArgoCD rows must survive a failed Flux read untouched.
			if snap.Summary.Total != before {
				t.Errorf("total = %d, want the argocd rows intact at %d", snap.Summary.Total, before)
			}
		})
	}
}

func TestAppendFluxKeepsThePartialRead(t *testing.T) {
	full := loadFluxFixture(t)
	partial := &kube.FluxList{Kustomizations: full.Kustomizations}

	var snap Snapshot
	snap.AppendFlux(partial, &kube.StatusError{Code: 403, Body: "helmreleases is forbidden"}, Options{})

	if snap.Summary.Total != 4 {
		t.Errorf("total = %d, want the 4 kustomizations that were readable", snap.Summary.Total)
	}
	if snap.Flux.Error == "" {
		t.Error("want the failed half reported")
	}
}

// recordingFluxLister counts calls so a test can prove a disabled source is never asked.
type recordingFluxLister struct {
	calls atomic.Int64
	list  *kube.FluxList
	err   error
}

func (r *recordingFluxLister) ListFlux(context.Context) (*kube.FluxList, error) {
	r.calls.Add(1)
	return r.list, r.err
}

func TestCollectorNeverCallsADisabledSource(t *testing.T) {
	argo := &fakeLister{list: loadFixture(t)}
	flux := &recordingFluxLister{list: loadFluxFixture(t)}

	// WithFlux is not called, which is what SOURCES=argocd wires up.
	c, _ := newTestCollector(t, argo, 15*time.Second)
	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if flux.calls.Load() != 0 {
		t.Errorf("flux was queried %d times with the source disabled", flux.calls.Load())
	}
	if snap.Flux != nil {
		t.Error("Snapshot.Flux must stay nil when the source is off")
	}
	if snap.Summary.Total != 14 {
		t.Errorf("total = %d, want the 14 argocd rows only", snap.Summary.Total)
	}
}

func TestCollectorSkipsArgoCDWhenOnlyFluxIsEnabled(t *testing.T) {
	argo := &fakeLister{list: loadFixture(t)}
	flux := &recordingFluxLister{list: loadFluxFixture(t)}

	// A nil Lister is what SOURCES=flux wires up: the ArgoCD API is never built.
	c, _ := newTestCollector(t, nil, 15*time.Second)
	c.WithFlux(flux)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if argo.calls.Load() != 0 {
		t.Errorf("argocd was queried %d times with the source disabled", argo.calls.Load())
	}
	if snap.Summary.Total != 9 {
		t.Errorf("total = %d, want the 9 flux rows", snap.Summary.Total)
	}
	// Every header field below comes from the root Application, and there is none.
	if snap.HasRoot || snap.Version != "" || snap.LastDeployedAt != "" || snap.Phase != "" {
		t.Errorf("want no root app facts, got %+v", snap)
	}
}

func TestCollectorFoldsBothSources(t *testing.T) {
	flux := &recordingFluxLister{list: loadFluxFixture(t)}
	c, clock := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithFlux(flux)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.Summary.Total != 14+9 {
		t.Errorf("total = %d, want 23", snap.Summary.Total)
	}
	if !snap.HasRoot {
		t.Error("the argocd root app should still be recognised")
	}

	// The Flux read is cached with the snapshot, not repeated per request.
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if flux.calls.Load() != 1 {
		t.Errorf("flux fetches = %d, want 1 inside the TTL", flux.calls.Load())
	}

	clock.advance(16 * time.Second)
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if flux.calls.Load() != 2 {
		t.Errorf("flux fetches = %d, want a refresh after the TTL", flux.calls.Load())
	}
}

func TestCollectorRendersArgoCDWhenFluxFails(t *testing.T) {
	flux := &recordingFluxLister{err: &kube.StatusError{Code: 403, Body: "helmreleases is forbidden"}}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithFlux(flux)

	snap, err := c.Get(context.Background())
	// A failing optional source must not surface as a page-level error.
	if err != nil {
		t.Fatalf("a failed flux read must not fail the snapshot: %v", err)
	}
	if snap.Summary.Total != 14 {
		t.Errorf("total = %d, want the argocd rows intact", snap.Summary.Total)
	}
	if snap.Flux == nil || !snap.Flux.Denied {
		t.Errorf("want the denial recorded as a note, got %+v", snap.Flux)
	}
	if snap.Stale {
		t.Error("a failed flux read must not mark the snapshot stale")
	}
}

func TestBuildCarriesTheEnabledSources(t *testing.T) {
	snap := Build(loadFixture(t), Options{RootAppName: "root-app", Sources: []Source{SourceArgoCD, SourceFlux}})
	if len(snap.Sources) != 2 || snap.Sources[0] != SourceArgoCD || snap.Sources[1] != SourceFlux {
		t.Errorf("sources = %v", snap.Sources)
	}
	for _, s := range snap.Services {
		if s.Source != SourceArgoCD {
			t.Errorf("%s: source = %q, want argocd", s.Name, s.Source)
		}
		if s.Namespace != "" || s.Kind != "" {
			t.Errorf("%s: argocd rows carry no namespace or kind, got %q/%q", s.Name, s.Namespace, s.Kind)
		}
	}
}

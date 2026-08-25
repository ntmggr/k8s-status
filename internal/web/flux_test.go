package web

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ntmggr/k8s-status/internal/argocd"
	"github.com/ntmggr/k8s-status/internal/kube"
	"github.com/ntmggr/k8s-status/internal/status"
)

func fluxFixture(t *testing.T) *kube.FluxList {
	t.Helper()
	var out kube.FluxList

	var hrs kube.HelmReleaseList
	decodeFixture(t, "../../testdata/helmreleases.json", &hrs)
	out.HelmReleases = hrs.Items

	var ks kube.KustomizationList
	decodeFixture(t, "../../testdata/kustomizations.json", &ks)
	out.Kustomizations = ks.Items

	return &out
}

func decodeFixture(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// mixedSnapshot is an ArgoCD environment that also runs Flux.
func mixedSnapshot(t *testing.T, fluxErr error) *status.Snapshot {
	t.Helper()
	snap := fixtureSnapshot(t)
	snap.Sources = []status.Source{status.SourceArgoCD, status.SourceFlux}
	var list *kube.FluxList
	if fluxErr == nil {
		list = fluxFixture(t)
	}
	snap.AppendFlux(list, fluxErr, status.Options{})
	snap.CheckedAt = time.Now().Add(-4 * time.Second)
	return snap
}

// fluxOnlySnapshot is a cluster that runs Flux and no ArgoCD, so there is no root app.
func fluxOnlySnapshot(t *testing.T) *status.Snapshot {
	t.Helper()
	snap := status.Build(nil, status.Options{Sources: []status.Source{status.SourceFlux}})
	snap.AppendFlux(fluxFixture(t), nil, status.Options{})
	snap.CheckedAt = time.Now().Add(-2 * time.Second)
	return &snap
}

func TestSourceColumnOnlyAppearsWithMoreThanOneSource(t *testing.T) {
	single := fixtureSnapshot(t)
	single.Sources = []status.Source{status.SourceArgoCD}

	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: single})
	body := get(t, h, "/k8s-status/").Body.String()
	if strings.Contains(body, "<th class=\"hide-sm\">Source</th>") {
		t.Error("a single-source cluster must not grow a Source column")
	}
	if strings.Contains(body, `class="src"`) {
		t.Error("a single-source cluster must not render source chips")
	}

	h = newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: mixedSnapshot(t, nil)})
	body = get(t, h, "/k8s-status/").Body.String()
	if !strings.Contains(body, "<th class=\"hide-sm\">Source</th>") {
		t.Error("two sources should add the Source column")
	}
	for _, want := range []string{`<span class="src" title="HelmRelease">flux</span>`, `<span class="src" title="ArgoCD Application">argocd</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing chip %q", want)
		}
	}
}

func TestFluxReadFailureStillRendersArgoCD(t *testing.T) {
	snap := mixedSnapshot(t, &kube.StatusError{Code: 403, Body: "helmreleases.helm.toolkit.fluxcd.io is forbidden"})
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: snap})

	rec := get(t, h, "/k8s-status/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// Every ArgoCD row is still there.
	for _, name := range []string{"accounts-api", "media-encoder", "orders-api"} {
		if !strings.Contains(body, name) {
			t.Errorf("argocd row %q missing after a failed flux read", name)
		}
	}
	if !strings.Contains(body, "Flux rows need") || !strings.Contains(body, "helmreleases.helm.toolkit.fluxcd.io") {
		t.Error("want an inline note naming the missing permission")
	}
	if strings.Contains(body, `class="banner err"`) {
		t.Error("an optional source must degrade inline, not raise the page-level error banner")
	}

	rec = get(t, h, "/k8s-status/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("api status = %d, want 200", rec.Code)
	}
	var resp struct {
		Summary  struct{ Total int } `json:"summary"`
		Flux     *struct{ Error string }
		Services []struct{ Name string }
		Error    *string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("api error = %v, want null", *resp.Error)
	}
	if resp.Summary.Total != 14 {
		t.Errorf("total = %d, want the 14 argocd rows", resp.Summary.Total)
	}
	if resp.Flux == nil || resp.Flux.Error == "" {
		t.Error("want the flux failure reported under its own key")
	}
}

func TestFluxOnlyOmitsTheRootAppHeader(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fluxOnlySnapshot(t)})
	body := get(t, h, "/k8s-status/").Body.String()

	// Everything on that line comes from the ArgoCD root Application, so with no root
	// app the whole line goes rather than rendering "unknown" and a row of blanks.
	if strings.Contains(body, `class="sub"`) {
		t.Error("the environment header should be omitted when there is no root app")
	}
	if strings.Contains(body, `<strong class="mono">unknown</strong>`) {
		t.Error("the version must not render as unknown on a flux-only cluster")
	}
	if strings.Contains(body, "last deploy") {
		t.Error("there is no deploy history without a root app")
	}

	// The table itself is unaffected.
	for _, name := range []string{"orders-api", "platform-config", "archive-sync"} {
		if !strings.Contains(body, name) {
			t.Errorf("flux row %q missing", name)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("the page must stay script-free")
	}
}

func TestArgoCDOnlyStillReportsAMisconfiguredRootApp(t *testing.T) {
	// ROOT_APP_NAME matching nothing is a documented misconfiguration whose symptom is
	// the version reading "unknown". Hiding the line would hide the diagnosis.
	var list argocd.ApplicationList
	decodeFixture(t, "../../testdata/applications.json", &list)
	snap := status.Build(&list, status.Options{
		RootAppName: "no-such-root",
		Sources:     []status.Source{status.SourceArgoCD},
	})
	snap.CheckedAt = time.Now()

	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: &snap})
	body := get(t, h, "/k8s-status/").Body.String()
	if !strings.Contains(body, `class="sub"`) || !strings.Contains(body, `<strong class="mono">unknown</strong>`) {
		t.Error("an argocd cluster with no matching root app should still show the version line as unknown")
	}
}

func TestFluxRowsCarrySourceNamespaceAndKindInJSON(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: mixedSnapshot(t, nil)})
	rec := get(t, h, "/k8s-status/api/status")

	var resp struct {
		Sources  []string `json:"sources"`
		Flux     *struct{ HelmReleases, Kustomizations int }
		Services []struct {
			Name, Source, Namespace, Kind, State, Version, AppVersion, Revision string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Sources) != 2 {
		t.Errorf("sources = %v", resp.Sources)
	}
	if resp.Flux == nil || resp.Flux.HelmReleases != 5 || resp.Flux.Kustomizations != 4 {
		t.Errorf("flux = %+v", resp.Flux)
	}

	byKey := map[string]struct {
		Name, Source, Namespace, Kind, State, Version, AppVersion, Revision string
	}{}
	for _, s := range resp.Services {
		byKey[s.Source+"/"+s.Name] = s
	}

	hr, ok := byKey["flux/orders-api"]
	if !ok {
		t.Fatal("the flux HelmRelease row is missing")
	}
	if hr.Kind != "HelmRelease" || hr.Namespace != "sample-apps" || hr.State != "OK" {
		t.Errorf("helmrelease row = %+v", hr)
	}

	ks, ok := byKey["flux/platform-config"]
	if !ok {
		t.Fatal("the flux Kustomization row is missing")
	}
	if ks.Version != "master" || ks.Revision != "eec06d1ea459af4cb4e10e806f8be7c7bd58b361" {
		t.Errorf("kustomization row = %+v", ks)
	}

	// An ArgoCD row of the same name is a separate row, not a merged one.
	argo, ok := byKey["argocd/orders-api"]
	if !ok {
		t.Fatal("the argocd row of the same name is missing")
	}
	if argo.Namespace != "" || argo.Kind != "" {
		t.Errorf("argocd rows must carry no namespace or kind, got %+v", argo)
	}
}

func TestFluxRowsAreFilterable(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: mixedSnapshot(t, nil)})

	rec := get(t, h, "/k8s-status/api/status?status=SUSPENDED")
	var resp struct {
		Services []struct{ Name, Source string }
		Filters  *struct{ Matched int }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Filters == nil || resp.Filters.Matched != len(resp.Services) {
		t.Errorf("filters = %+v, services = %d", resp.Filters, len(resp.Services))
	}
	seen := map[string]bool{}
	for _, s := range resp.Services {
		seen[s.Name] = true
	}
	for _, name := range []string{"legacy-exporter", "archive-sync"} {
		if !seen[name] {
			t.Errorf("suspended flux row %q should be selectable by the status filter", name)
		}
	}

	// Flux rows report no sync state, so ?sync=Unknown is how they are selected.
	rec = get(t, h, "/k8s-status/api/status?sync=Unknown")
	resp.Services = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Services) < 9 {
		t.Errorf("sync=Unknown matched %d rows, want at least the 9 flux ones", len(resp.Services))
	}
}

package kube

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// fluxHandler serves the two Flux collection endpoints from the fixtures, and records
// which paths were asked for so a test can assert what was and was not called.
func fluxHandler(t *testing.T, calls *[]string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case helmReleasesPath:
			http.ServeFile(w, r, "../../testdata/helmreleases.json")
		case kustomizationsPath:
			http.ServeFile(w, r, "../../testdata/kustomizations.json")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestListFluxParsesFixtures(t *testing.T) {
	var calls []string
	c := newTestClient(t, fluxHandler(t, &calls))

	list, err := c.ListFlux(context.Background())
	if err != nil {
		t.Fatalf("ListFlux: %v", err)
	}

	if len(list.HelmReleases) != 5 {
		t.Fatalf("helmreleases = %d, want 5", len(list.HelmReleases))
	}
	if len(list.Kustomizations) != 4 {
		t.Fatalf("kustomizations = %d, want 4", len(list.Kustomizations))
	}

	// The endpoints are the cluster-wide collections: no namespace segment.
	want := map[string]bool{
		"/apis/helm.toolkit.fluxcd.io/v2/helmreleases":        false,
		"/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations": false,
	}
	for _, p := range calls {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected request to %q", p)
			continue
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("endpoint %q was never called", p)
		}
	}

	hr := map[string]HelmRelease{}
	for _, h := range list.HelmReleases {
		hr[h.Metadata.Name] = h
	}

	ok := hr["orders-api"]
	if ok.Metadata.Namespace != "sample-apps" {
		t.Errorf("namespace = %q", ok.Metadata.Namespace)
	}
	if ok.Spec.Chart.Spec.Version != "6.5.4" {
		t.Errorf("spec.chart.spec.version = %q", ok.Spec.Chart.Spec.Version)
	}
	if len(ok.Status.History) == 0 || ok.Status.History[0].AppVersion != "6.5.4" {
		t.Errorf("history = %+v", ok.Status.History)
	}
	// helm.toolkit.fluxcd.io/v2 has no lastAppliedRevision at all: it records the chart
	// version it attempted, not a git revision.
	if ok.Status.LastAppliedRevision != "" {
		t.Errorf("lastAppliedRevision = %q, want empty on the v2 API", ok.Status.LastAppliedRevision)
	}
	if ok.Status.LastAttemptedRevision != "6.5.4" {
		t.Errorf("lastAttemptedRevision = %q", ok.Status.LastAttemptedRevision)
	}

	if !hr["legacy-exporter"].Spec.Suspend {
		t.Error("legacy-exporter should decode spec.suspend as true")
	}
	// A suspended object carries no conditions at all, which is why suspend has to be
	// read from the spec rather than inferred from the status.
	if len(hr["legacy-exporter"].Status.Conditions) != 0 {
		t.Errorf("suspended release conditions = %+v", hr["legacy-exporter"].Status.Conditions)
	}

	ks := map[string]Kustomization{}
	for _, k := range list.Kustomizations {
		ks[k.Metadata.Name] = k
	}
	if got := ks["platform-config"].Status.LastAppliedRevision; got != "master@sha1:eec06d1ea459af4cb4e10e806f8be7c7bd58b361" {
		t.Errorf("kustomization lastAppliedRevision = %q", got)
	}
	if !ks["archive-sync"].Spec.Suspend {
		t.Error("archive-sync should decode spec.suspend as true")
	}
}

func TestListFluxKeepsTheKindThatSucceeded(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == helmReleasesPath {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("helmreleases is forbidden"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, "../../testdata/kustomizations.json")
	}))

	list, err := c.ListFlux(context.Background())
	if err == nil {
		t.Fatal("want an error for the denied kind")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
	if len(list.Kustomizations) != 4 {
		t.Errorf("kustomizations = %d, want the successful kind kept", len(list.Kustomizations))
	}
	if len(list.HelmReleases) != 0 {
		t.Errorf("helmreleases = %d, want none", len(list.HelmReleases))
	}
}

func TestListFluxMissingCRDs(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 page not found"))
	}))

	list, err := c.ListFlux(context.Background())
	if err == nil {
		t.Fatal("want an error when the groups are not served")
	}
	if list == nil {
		t.Fatal("a failed read must still return a list so callers can render the rest")
	}
	if len(list.HelmReleases)+len(list.Kustomizations) != 0 {
		t.Error("want no items")
	}
}

func TestHasResourceDetectsPresenceAndAbsence(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/"+HelmReleaseGroupVersion {
			// An absent CRD group is a 404 from discovery, not an error.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"helm.toolkit.fluxcd.io/v2",
			"resources":[{"name":"helmreleases","kind":"HelmRelease"},
			             {"name":"helmreleases/status","kind":"HelmRelease"}]}`))
	}))

	ctx := context.Background()

	got, err := c.HasResource(ctx, HelmReleaseGroupVersion, ResourceHelmReleases)
	if err != nil || !got {
		t.Errorf("helmreleases: got %v, err %v; want true", got, err)
	}
	got, err = c.HasResource(ctx, KustomizationGroupVersion, ResourceKustomizations)
	if err != nil {
		t.Errorf("an absent group must not be an error, got %v", err)
	}
	if got {
		t.Error("kustomizations should be reported absent")
	}
	got, err = c.HasResource(ctx, HelmReleaseGroupVersion, "receivers")
	if err != nil || got {
		t.Errorf("a served group missing the resource: got %v, err %v; want false", got, err)
	}
}

func TestHasResourceReportsRealFailures(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if _, err := c.HasResource(context.Background(), HelmReleaseGroupVersion, ResourceHelmReleases); err == nil {
		t.Fatal("a 500 must not be silently reported as absent")
	}
}

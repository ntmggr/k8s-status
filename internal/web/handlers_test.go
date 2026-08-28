package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ntmggr/k8s-status/internal/argocd"
	"github.com/ntmggr/k8s-status/internal/kube"
	"github.com/ntmggr/k8s-status/internal/status"
)

type fakeProvider struct {
	snap *status.Snapshot
	err  error
}

func (f fakeProvider) Get(context.Context) (*status.Snapshot, error) { return f.snap, f.err }

func fixtureSnapshot(t *testing.T) *status.Snapshot {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/applications.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var list argocd.ApplicationList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	snap := status.Build(&list, status.Options{RootAppName: "root-app"})
	snap.CheckedAt = time.Now().Add(-4 * time.Second)
	return &snap
}

func newTestServer(t *testing.T, cfg Config, p Provider) http.Handler {
	t.Helper()
	if cfg.EnvName == "" {
		cfg.EnvName = "sample-dev"
	}
	if cfg.RefreshSeconds == 0 {
		cfg.RefreshSeconds = 30
	}
	s, err := NewServer(cfg, p)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s.Routes()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthzIndependentOfClusterRead(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{err: errors.New("connection refused")})

	rec := get(t, h, "/k8s-status/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

func TestAPIReturns200WithErrorOnFailure(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status", Region: "eu-west-1", ClusterName: "sample-dev-cluster"},
		fakeProvider{err: errors.New("kubernetes api returned 503")})

	rec := get(t, h, "/k8s-status/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] == nil {
		t.Error("error should be non-null on failure")
	}
	svcs, ok := resp["services"].([]any)
	if !ok || len(svcs) != 0 {
		t.Errorf("services = %v, want empty array", resp["services"])
	}
	if resp["schema"] != float64(1) {
		t.Errorf("schema = %v, want 1", resp["schema"])
	}
	if resp["clusterName"] != "sample-dev-cluster" || resp["clusterPath"] != "sample-dev-cluster" {
		t.Errorf("cluster fields = %v / %v", resp["clusterName"], resp["clusterPath"])
	}
}

func TestAPISuccessShape(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status", Region: "eu-west-1", ClusterName: "sample-dev-cluster"},
		fakeProvider{snap: fixtureSnapshot(t)})

	rec := get(t, h, "/k8s-status/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp struct {
		Schema     int     `json:"schema"`
		Env        string  `json:"env"`
		EnvType    string  `json:"envType"`
		Version    string  `json:"version"`
		RootHealth string  `json:"rootHealth"`
		AgeSeconds int     `json:"ageSeconds"`
		Stale      bool    `json:"stale"`
		Error      *string `json:"error"`
		Summary    struct {
			Total, OK, Degraded, Warning, Progressing, Drift, Prune, Suspended, Hidden int
			Health                                                                     struct {
				Percent int  `json:"percent"`
				Known   bool `json:"known"`
			} `json:"health"`
		} `json:"summary"`
		Services []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"services"`
		LastDeployID int `json:"lastDeployId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Error != nil {
		t.Errorf("error = %v, want null", *resp.Error)
	}
	if resp.EnvType != "dev" {
		t.Errorf("envType = %q, want dev (root label fallback)", resp.EnvType)
	}
	if resp.Version != "develop" || resp.RootHealth != "Degraded" || resp.LastDeployID != 1048 {
		t.Errorf("root fields = %q/%q/%d", resp.Version, resp.RootHealth, resp.LastDeployID)
	}
	if resp.Summary.Total != 14 || resp.Summary.Degraded != 2 || resp.Summary.Warning != 1 || resp.Summary.Drift != 1 || resp.Summary.Prune != 2 {
		t.Errorf("summary = %+v", resp.Summary)
	}
	// 6 OK out of (14 total - 2 prune - 1 suspended) = 11 counted, rounds to 55%.
	if !resp.Summary.Health.Known || resp.Summary.Health.Percent != 55 {
		t.Errorf("summary.health = %+v, want known=true percent=55", resp.Summary.Health)
	}
	if len(resp.Services) != 14 {
		t.Fatalf("services = %d, want 14", len(resp.Services))
	}
	if resp.Services[0].State != "DEGRADED" {
		t.Errorf("first service state = %q, want DEGRADED", resp.Services[0].State)
	}
	if resp.AgeSeconds < 3 || resp.AgeSeconds > 60 {
		t.Errorf("ageSeconds = %d, want a small positive value", resp.AgeSeconds)
	}
	if resp.Stale {
		t.Error("stale should be false")
	}
}

func TestBadgeShowsClusterOverEnvType(t *testing.T) {
	base := Config{BasePath: "/k8s-status", EnvName: "local", RefreshSeconds: 30}

	withCluster := base
	withCluster.ClusterName = "k8s-stage"
	h := newTestServer(t, withCluster, fakeProvider{snap: fixtureSnapshot(t)})
	body := get(t, h, "/k8s-status/").Body.String()
	if !strings.Contains(body, `>k8s-stage</span>`) {
		t.Errorf("badge should show the cluster/context when known")
	}
	if strings.Contains(body, `class="badge is-envtype"`) {
		t.Errorf("badge should not fall back to env type when a cluster is known")
	}

	// With no cluster configured the env type is still better than nothing.
	h = newTestServer(t, base, fakeProvider{snap: fixtureSnapshot(t)})
	if body := get(t, h, "/k8s-status/").Body.String(); !strings.Contains(body, `class="badge is-envtype"`) {
		t.Errorf("badge should fall back to env type when no cluster is set")
	}
}

func TestPageRenders(t *testing.T) {
	h := newTestServer(t, Config{
		BasePath:       "/k8s-status",
		EnvName:        "sample-dev",
		Region:         "eu-west-1",
		ArgoCDUIBase:   "https://argocd.example.invalid/",
		RefreshSeconds: 30,
		BuildVersion:   "0.1.0",
	}, fakeProvider{snap: fixtureSnapshot(t)})

	rec := get(t, h, "/k8s-status/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"sample-dev", "eu-west-1", "class=\"badge is-envtype\"",
		`<meta http-equiv="refresh" content="30">`,
		"DEGRADED", "PROGRESSING", "PRUNE", "SUSPENDED", "OK",
		"s-degraded", "s-warning", "s-drift", "s-prune",
		"media-encoder", "0/2 replicas available",
		`href="https://argocd.example.invalid/applications/accounts-api"`,
		"437a162", "k8s-status 0.1.0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("page must not contain JavaScript")
	}
}

func TestPageWithoutArgoCDUIHasNoServiceLinks(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})
	body := get(t, h, "/k8s-status/").Body.String()
	if strings.Contains(body, "/applications/accounts-api") {
		t.Error("services should be plain text when ARGOCD_UI_BASE is unset")
	}
	if !strings.Contains(body, "accounts-api") {
		t.Error("service name should still be rendered")
	}
}

func TestPageErrorAndStaleBanners(t *testing.T) {
	stale := fixtureSnapshot(t)
	stale.Stale = true

	h := newTestServer(t, Config{BasePath: "/k8s-status"},
		fakeProvider{snap: stale, err: errors.New("kubernetes api returned 503")})

	body := get(t, h, "/k8s-status/").Body.String()
	if !strings.Contains(body, "Could not read the cluster") {
		t.Error("missing error banner")
	}
	if !strings.Contains(body, "stale") {
		t.Error("missing stale banner")
	}
}

func TestPageRendersWhenClusterReadFails(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{err: errors.New("connection refused")})

	rec := get(t, h, "/k8s-status/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// "connection refused" is classified, so the banner names the actual cause.
	if !strings.Contains(rec.Body.String(), "Cannot reach the Kubernetes API") {
		t.Error("missing error banner")
	}
}

func TestBasePathRedirect(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	rec := get(t, h, "/k8s-status")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/k8s-status/" {
		t.Errorf("location = %q, want /k8s-status/", loc)
	}
}

func TestRootBasePath(t *testing.T) {
	h := newTestServer(t, Config{BasePath: ""}, fakeProvider{snap: fixtureSnapshot(t)})

	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", rec.Code)
	}
	if rec := get(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", rec.Code)
	}
	if rec := get(t, h, "/api/status"); rec.Code != http.StatusOK {
		t.Errorf("GET /api/status = %d, want 200", rec.Code)
	}
	if rec := get(t, h, "/k8s-status/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /k8s-status/ = %d, want 404", rec.Code)
	}
}

func TestBasePathNormalization(t *testing.T) {
	cases := map[string]string{
		"":             "",
		"/":            "",
		"k8s-status":   "/k8s-status",
		"/k8s-status":  "/k8s-status",
		"/k8s-status/": "/k8s-status",
		"  /status/  ": "/status",
	}
	for in, want := range cases {
		if got := NormalizeBasePath(in); got != want {
			t.Errorf("NormalizeBasePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanize(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "just now"},
		{6 * time.Minute, "6m ago"},
		{3 * time.Hour, "3h ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, tc := range cases {
		if got := humanize(tc.d); got != tc.want {
			t.Errorf("humanize(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("437a162c4f9b8d0e5a1c73f620b8e94d1a6c0f22"); got != "437a162" {
		t.Errorf("shortSHA = %q", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("shortSHA short input = %q", got)
	}
}

// nodeStatsProvider drives the whole chain: a real Collector with a fake nodes client,
// so the wiring, not just the template, is under test.
func nodeStatsProvider(t *testing.T, nodes status.NodeLister) Provider {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/applications.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var list argocd.ApplicationList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	c := status.NewCollector(staticLister{list: &list}, status.Options{RootAppName: "root-app"}, time.Minute)
	if nodes != nil {
		c.WithNodes(nodes)
	}
	return c
}

type staticLister struct{ list *argocd.ApplicationList }

func (s staticLister) ListApplications(context.Context) (*argocd.ApplicationList, error) {
	return s.list, nil
}

type stubNodeLister struct {
	called int
	list   *kube.NodeList
	err    error
}

func (s *stubNodeLister) ListNodes(context.Context) (*kube.NodeList, error) {
	s.called++
	return s.list, s.err
}

// meshProvider drives the whole chain: a real Collector with a fake mesh client, so
// the wiring, not just the template, is under test.
func meshProvider(t *testing.T, mesh status.MeshLister) Provider {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/applications.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var list argocd.ApplicationList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	c := status.NewCollector(staticLister{list: &list}, status.Options{RootAppName: "root-app"}, time.Minute)
	if mesh != nil {
		c.WithMesh(mesh, "istio-system")
	}
	return c
}

type stubMeshLister struct {
	groupVersion string
	detectErr    error
	pa           *kube.PeerAuthentication
	policyErr    error
}

func (s *stubMeshLister) DetectIstio(context.Context) (string, error) {
	return s.groupVersion, s.detectErr
}

func (s *stubMeshLister) MeshPolicy(context.Context, string, string) (*kube.PeerAuthentication, error) {
	return s.pa, s.policyErr
}

// unmanagedProvider drives the whole chain: a real Collector with a fake workloads
// client, so the wiring, not just the template, is under test.
func unmanagedProvider(t *testing.T, workloads status.WorkloadLister) Provider {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/applications.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var list argocd.ApplicationList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	c := status.NewCollector(staticLister{list: &list}, status.Options{RootAppName: "root-app"}, time.Minute)
	if workloads != nil {
		c.WithUnmanaged(workloads)
	}
	return c
}

type stubWorkloadLister struct {
	called int
	list   *kube.WorkloadList
	err    error
}

func (s *stubWorkloadLister) ListWorkloads(context.Context) (*kube.WorkloadList, error) {
	s.called++
	return s.list, s.err
}

func sampleWorkloads() *kube.WorkloadList {
	return &kube.WorkloadList{Items: []kube.Workload{
		{Kind: kube.KindDeployment,
			Metadata: kube.WorkloadMetadata{Name: "csi-controller", Namespace: "kube-system",
				Labels: map[string]string{"app.kubernetes.io/managed-by": "EKS"}},
			Spec:   kube.WorkloadSpec{Template: kube.PodTemplate{Spec: kube.PodSpec{Containers: []kube.Container{{Image: "registry.invalid/csi:v1.2.3"}}}}},
			Status: kube.WorkloadStatus{Replicas: 2, ReadyReplicas: 2}},
		{Kind: kube.KindDaemonSet,
			Metadata: kube.WorkloadMetadata{Name: "csi-node-windows", Namespace: "kube-system"},
			Status:   kube.WorkloadStatus{}},
		{Kind: kube.KindDaemonSet,
			Metadata: kube.WorkloadMetadata{Name: "kube-proxy", Namespace: "kube-system"},
			Status:   kube.WorkloadStatus{DesiredNumberScheduled: 82, NumberReady: 81}},
		// Managed by ArgoCD, so it must not appear at all.
		{Kind: kube.KindDeployment,
			Metadata: kube.WorkloadMetadata{Name: "accounts-api", Namespace: "apps",
				Labels: map[string]string{"app.kubernetes.io/instance": "accounts-api"}},
			Status: kube.WorkloadStatus{Replicas: 1, ReadyReplicas: 1}},
	}}
}

// UNMANAGED=false must render no section and never call the API.
func TestPageWithoutUnmanagedHasNoSection(t *testing.T) {
	workloads := &stubWorkloadLister{list: sampleWorkloads()}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, unmanagedProvider(t, nil))

	body := get(t, h, "/k8s-status/").Body.String()
	if strings.Contains(body, "Not managed by ArgoCD") {
		t.Error("unmanaged section rendered while UNMANAGED is off")
	}
	if strings.Contains(body, "unmanaged workloads") {
		t.Error("unmanaged count rendered while UNMANAGED is off")
	}
	if workloads.called != 0 {
		t.Errorf("workloads API called %d times, want 0", workloads.called)
	}
	if !strings.Contains(body, "accounts-api") {
		t.Error("service table should still render")
	}
}

func TestPageWithUnmanagedRendersSection(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, unmanagedProvider(t, &stubWorkloadLister{list: sampleWorkloads()}))

	rec := get(t, h, "/k8s-status/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Not managed by ArgoCD", "not in gitops",
		"csi-controller", "csi-node-windows", "kube-proxy",
		"EKS", "unknown", "v1.2.3", "81/82", "0/0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("unmanaged section missing %q", want)
		}
	}
	// The ArgoCD-managed Deployment carries a marker and must be excluded.
	if !strings.Contains(body, ">3 not in gitops</a>") {
		t.Errorf("want a count of 3 unmanaged workloads")
	}
	// desired == 0 is deliberate, not broken.
	if !strings.Contains(body, "s-suspended") {
		t.Error("a 0/0 workload should render SUSPENDED, not DEGRADED")
	}
	if strings.Contains(body, "<script") {
		t.Error("page must not contain JavaScript")
	}
}

// The status tiles are mutually exclusive and sum to the service total; unmanaged
// workloads are not services and must stay out of that row.
func TestUnmanagedIsNotAStatusTile(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, unmanagedProvider(t, &stubWorkloadLister{list: sampleWorkloads()}))

	body := get(t, h, "/k8s-status/").Body.String()
	start := strings.Index(body, `<div class="tiles">`)
	// Bound this to the status tile row itself. The separate "views" row below it
	// deliberately does carry unmanaged, because those are cross-cutting selections
	// rather than states that sum to the service total.
	end := strings.Index(body, `<div class="views">`)
	if end < 0 {
		end = strings.Index(body, `<details class="legend">`)
	}
	if start < 0 || end < start {
		t.Fatal("could not locate the status tile row")
	}
	if strings.Contains(body[start:end], "unmanaged") {
		t.Error("unmanaged must not be rendered as a status tile")
	}
}

func TestPageStillRendersWhenWorkloadsForbidden(t *testing.T) {
	workloads := &stubWorkloadLister{err: &kube.StatusError{Code: http.StatusForbidden, Body: "deployments is forbidden"}}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, unmanagedProvider(t, workloads))

	rec := get(t, h, "/k8s-status/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ClusterRole") {
		t.Error("want an inline note explaining the missing ClusterRole")
	}
	if strings.Contains(body, "cluster read failed") {
		t.Error("a denied workloads read must not raise the page-level error banner")
	}
	for _, want := range []string{"accounts-api", "media-encoder", "0/2 replicas available"} {
		if !strings.Contains(body, want) {
			t.Errorf("application data lost from the page: missing %q", want)
		}
	}
}

func TestAPIUnmanagedObjectOmittedWhenDisabled(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, unmanagedProvider(t, nil))

	body := get(t, h, "/k8s-status/api/status").Body.String()
	if strings.Contains(body, `"unmanaged"`) {
		t.Errorf("unmanaged object should be omitted when the feature is off: %s", body)
	}
}

func TestAPIUnmanagedObject(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, unmanagedProvider(t, &stubWorkloadLister{list: sampleWorkloads()}))

	var resp struct {
		Unmanaged *struct {
			Count   int `json:"count"`
			Scanned int `json:"scanned"`
			Items   []struct {
				Namespace string `json:"namespace"`
				Kind      string `json:"kind"`
				Name      string `json:"name"`
				ManagedBy string `json:"managedBy"`
				Ready     int    `json:"ready"`
				Desired   int    `json:"desired"`
				Version   string `json:"version"`
				State     string `json:"state"`
			} `json:"items"`
			Error string `json:"error"`
		} `json:"unmanaged"`
		Summary struct {
			Total int `json:"total"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(get(t, h, "/k8s-status/api/status").Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Unmanaged == nil {
		t.Fatal("want an unmanaged object")
	}
	if resp.Unmanaged.Count != 3 || resp.Unmanaged.Scanned != 4 {
		t.Errorf("count = %d, scanned = %d, want 3 and 4", resp.Unmanaged.Count, resp.Unmanaged.Scanned)
	}
	if resp.Summary.Total != 14 {
		t.Errorf("service total = %d, want 14: unmanaged workloads must not join the summary", resp.Summary.Total)
	}
	byName := map[string]string{}
	for _, w := range resp.Unmanaged.Items {
		byName[w.Name] = w.State
	}
	if byName["csi-node-windows"] != "SUSPENDED" {
		t.Errorf("csi-node-windows = %q, want SUSPENDED", byName["csi-node-windows"])
	}
	if byName["kube-proxy"] != "DEGRADED" {
		t.Errorf("kube-proxy = %q, want DEGRADED", byName["kube-proxy"])
	}
	if byName["csi-controller"] != "OK" {
		t.Errorf("csi-controller = %q, want OK", byName["csi-controller"])
	}
	if resp.Unmanaged.Error != "" {
		t.Errorf("error = %q, want empty", resp.Unmanaged.Error)
	}
}

func TestPageWithoutNodeStatsHasNoCapacitySection(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, nodeStatsProvider(t, nil))

	rec := get(t, h, "/k8s-status/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Cluster capacity") {
		t.Error("capacity section rendered while NODE_STATS is off")
	}
	if !strings.Contains(body, "accounts-api") {
		t.Error("service table should still render")
	}
	// GPU is orthogonal to the mutually exclusive status tiles and must not sit in that row.
	if strings.Contains(body, `class="k">gpu<`) {
		t.Error("GPU must not be rendered as a status tile")
	}
}

func TestPageWithNodeStatsRendersCapacitySection(t *testing.T) {
	nodes := &stubNodeLister{list: &kube.NodeList{Items: []kube.Node{
		{Status: kube.NodeStatus{Capacity: map[string]kube.Quantity{"nvidia.com/gpu": "2"}, NodeInfo: kube.NodeInfo{Architecture: "amd64"}}},
		{Status: kube.NodeStatus{NodeInfo: kube.NodeInfo{Architecture: "arm64"}}},
		{Status: kube.NodeStatus{NodeInfo: kube.NodeInfo{Architecture: "arm64"}}},
	}}}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, nodeStatsProvider(t, nodes))

	body := get(t, h, "/k8s-status/").Body.String()
	for _, want := range []string{"Cluster", "nodes", "cpu", "gpu", "cards", "2 arm64", "1 amd64"} {
		if !strings.Contains(body, want) {
			t.Errorf("capacity section missing %q", want)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("page must not contain JavaScript")
	}
}

// A MIG-partitioned cluster advertises slices and no nvidia.com/gpu at all, so the
// page has to name what it found rather than saying "gpu" and "cards" regardless.
func TestPageNamesNonNvidiaAccelerators(t *testing.T) {
	nodes := &stubNodeLister{list: &kube.NodeList{Items: []kube.Node{
		{Status: kube.NodeStatus{Capacity: map[string]kube.Quantity{"nvidia.com/mig-1g.5gb": "7"}, NodeInfo: kube.NodeInfo{Architecture: "amd64"}}},
		{Status: kube.NodeStatus{NodeInfo: kube.NodeInfo{Architecture: "arm64"}}},
	}}}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, nodeStatsProvider(t, nodes))

	body := get(t, h, "/k8s-status/").Body.String()
	// The chip leads with the running count rather than the label, so the accelerator
	// name is asserted where it still belongs: the capacity line and its tooltip.
	for _, want := range []string{"1 mig", "7 devices", "nvidia.com/mig-1g.5gb 7"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q for a MIG cluster", want)
		}
	}
	// "cards" is wrong for a slice, and the old wording must be gone.
	if strings.Contains(body, "7 cards") {
		t.Error("MIG slices must not be counted as cards")
	}
	if strings.Contains(body, "<script") {
		t.Error("page must not contain JavaScript")
	}
}

func TestPageStillRendersWhenNodesForbidden(t *testing.T) {
	nodes := &stubNodeLister{err: &kube.StatusError{Code: http.StatusForbidden, Body: "nodes is forbidden"}}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, nodeStatsProvider(t, nodes))

	rec := get(t, h, "/k8s-status/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ClusterRole") {
		t.Error("want an inline note explaining the missing ClusterRole")
	}
	if strings.Contains(body, "cluster read failed") {
		t.Error("a denied nodes read must not raise the page-level error banner")
	}
	for _, want := range []string{"accounts-api", "media-encoder", "0/2 replicas available"} {
		if !strings.Contains(body, want) {
			t.Errorf("application data lost from the page: missing %q", want)
		}
	}
}

func TestAPINodesObjectOmittedWhenDisabled(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, nodeStatsProvider(t, nil))

	body := get(t, h, "/k8s-status/api/status").Body.String()
	if strings.Contains(body, `"nodes"`) {
		t.Errorf("nodes object should be omitted when the feature is off: %s", body)
	}
}

func TestAPINodesObject(t *testing.T) {
	nodes := &stubNodeLister{list: &kube.NodeList{Items: []kube.Node{
		{Status: kube.NodeStatus{Capacity: map[string]kube.Quantity{"nvidia.com/gpu": "4"}, NodeInfo: kube.NodeInfo{Architecture: "amd64"}}},
		{Status: kube.NodeStatus{NodeInfo: kube.NodeInfo{Architecture: "arm64"}}},
	}}}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, nodeStatsProvider(t, nodes))

	var resp struct {
		Nodes *struct {
			Total       int            `json:"total"`
			CPUNodes    int            `json:"cpuNodes"`
			GPUNodes    int            `json:"gpuNodes"`
			GPUs        int            `json:"gpus"`
			GPUServices int            `json:"gpuServices"`
			Arch        map[string]int `json:"arch"`
			Error       string         `json:"error"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(get(t, h, "/k8s-status/api/status").Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Nodes == nil {
		t.Fatal("want a nodes object")
	}
	if resp.Nodes.Total != 2 || resp.Nodes.GPUNodes != 1 || resp.Nodes.CPUNodes != 1 || resp.Nodes.GPUs != 4 {
		t.Errorf("nodes = %+v", *resp.Nodes)
	}
	if resp.Nodes.Arch["amd64"] != 1 || resp.Nodes.Arch["arm64"] != 1 {
		t.Errorf("arch = %+v", resp.Nodes.Arch)
	}
	if resp.Nodes.Error != "" {
		t.Errorf("error = %q, want empty", resp.Nodes.Error)
	}
}

func TestAPIMeshObjectOmittedWhenDisabled(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, meshProvider(t, nil))

	body := get(t, h, "/k8s-status/api/status").Body.String()
	if strings.Contains(body, `"mesh"`) {
		t.Errorf("mesh object should be omitted when the feature is off: %s", body)
	}
}

func TestAPIMeshObjectPresentWhenEnabled(t *testing.T) {
	mesh := &stubMeshLister{
		groupVersion: kube.IstioSecurityGroupVersion,
		pa:           &kube.PeerAuthentication{Spec: kube.PeerAuthenticationSpec{MTLS: kube.PeerAuthenticationMTLS{Mode: "STRICT"}}},
	}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, meshProvider(t, mesh))

	var resp struct {
		Mesh *struct {
			Installed bool   `json:"installed"`
			Effective string `json:"effective"`
		} `json:"mesh"`
	}
	if err := json.Unmarshal(get(t, h, "/k8s-status/api/status").Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Mesh == nil {
		t.Fatal("want a mesh object")
	}
	if !resp.Mesh.Installed || resp.Mesh.Effective != "strict" {
		t.Errorf("mesh = %+v", *resp.Mesh)
	}
}

func countRows(body string) int { return strings.Count(body, `<tbody class="svc`) }

func TestPageFiltersRows(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	all := get(t, h, "/k8s-status/").Body.String()
	if countRows(all) != 14 {
		t.Fatalf("unfiltered rows = %d, want 14", countRows(all))
	}

	rec := get(t, h, "/k8s-status/?status=DEGRADED")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if got := countRows(body); got != 2 {
		t.Errorf("DEGRADED rows = %d, want 2", got)
	}
	if !strings.Contains(body, "showing 2 of 14") {
		t.Error("want a showing-N-of-M count")
	}
	// Counts stay whole-cluster while a filter is applied.
	if !strings.Contains(body, `<div class="n">14</div><div class="k">services</div>`) {
		t.Error("the total tile must keep counting the whole cluster")
	}
	if !strings.Contains(body, `class="chip">status DEGRADED<`) {
		t.Error("want a removable chip for the active filter")
	}
}

func TestPageFilterCombinations(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	for _, tc := range []struct{ path string }{
		{"/k8s-status/?status=DEGRADED,DRIFT"},
		{"/k8s-status/?status=DEGRADED&status=DRIFT"},
		{"/k8s-status/?status=degraded,%20drift"},
	} {
		if got := countRows(get(t, h, tc.path).Body.String()); got != 3 {
			t.Errorf("%s rows = %d, want 3", tc.path, got)
		}
	}
	if got := countRows(get(t, h, "/k8s-status/?sync=OutOfSync").Body.String()); got != 4 {
		t.Errorf("sync=OutOfSync rows = %d, want 4", got)
	}
	if got := countRows(get(t, h, "/k8s-status/?sync=outofsync&status=DRIFT").Body.String()); got != 1 {
		t.Errorf("sync+status rows = %d, want 1", got)
	}
}

func TestPageUnknownFilterValueRendersEmptyState(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	rec := get(t, h, "/k8s-status/?status=BANANAS")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if got := countRows(body); got != 0 {
		t.Errorf("rows = %d, want 0, an unknown value must not show everything", got)
	}
	if !strings.Contains(body, "no services match") {
		t.Error("want an explicit empty state")
	}
	if !strings.Contains(body, "status BANANAS") {
		t.Error("want the offending filter echoed as a chip")
	}
}

func TestTileLinkTogglesActiveFilter(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	off := get(t, h, "/k8s-status/").Body.String()
	if !strings.Contains(off, `href="/k8s-status/?status=DEGRADED"`) {
		t.Error("inactive DEGRADED tile should link to the filtered view")
	}

	on := get(t, h, "/k8s-status/?status=DEGRADED").Body.String()
	if !strings.Contains(on, `class="tile t-degraded is-on" href="/k8s-status/"`) {
		t.Error("active DEGRADED tile should link back to the cleared view and be marked selected")
	}
}

func TestPageRefreshOverride(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status", RefreshSeconds: 30}, fakeProvider{snap: fixtureSnapshot(t)})

	cases := []struct {
		path string
		want string
	}{
		{"/k8s-status/", `<meta http-equiv="refresh" content="30">`},
		{"/k8s-status/?refresh=60", `<meta http-equiv="refresh" content="60">`},
		{"/k8s-status/?refresh=abc", `<meta http-equiv="refresh" content="30">`},
		{"/k8s-status/?refresh=-5", `<meta http-equiv="refresh" content="30">`},
		{"/k8s-status/?refresh=", `<meta http-equiv="refresh" content="30">`},
		{"/k8s-status/?refresh=1", `<meta http-equiv="refresh" content="5">`},
		{"/k8s-status/?refresh=999999", `<meta http-equiv="refresh" content="3600">`},
	}
	for _, tc := range cases {
		body := get(t, h, tc.path).Body.String()
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: want %q", tc.path, tc.want)
		}
	}

	off := get(t, h, "/k8s-status/?refresh=0").Body.String()
	if strings.Contains(off, "http-equiv=\"refresh\"") {
		t.Error("refresh=0 must omit the meta refresh tag entirely")
	}
}

func TestRefreshAndFilterCompose(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status", RefreshSeconds: 30}, fakeProvider{snap: fixtureSnapshot(t)})

	body := get(t, h, "/k8s-status/?status=DEGRADED&refresh=60").Body.String()

	if !strings.Contains(body, `<meta http-equiv="refresh" content="60">`) {
		t.Error("refresh override lost when a filter is active")
	}
	if countRows(body) != 2 {
		t.Errorf("rows = %d, want 2, filter lost when refresh is set", countRows(body))
	}
	// The chip's remove link keeps the refresh choice.
	if !strings.Contains(body, `href="/k8s-status/?refresh=60"`) {
		t.Error("chip removal link dropped refresh=60")
	}
	// The refresh links keep the filter.
	if !strings.Contains(body, `href="/k8s-status/?refresh=10&amp;status=DEGRADED"`) {
		t.Error("refresh links dropped status=DEGRADED")
	}
	if strings.Contains(body, "<script") {
		t.Error("page must not contain JavaScript")
	}
}

func TestAPIFiltersAndEchoesTheFilter(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	var resp struct {
		Summary struct {
			Total int `json:"total"`
		} `json:"summary"`
		Filters *struct {
			Status  []string `json:"status"`
			Sync    []string `json:"sync"`
			GPU     string   `json:"gpu"`
			Matched int      `json:"matched"`
		} `json:"filters"`
		Services []struct {
			State string `json:"state"`
		} `json:"services"`
	}
	rec := get(t, h, "/k8s-status/api/status?status=DEGRADED,DRIFT&sync=OutOfSync")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Filters == nil {
		t.Fatal("want a filters object")
	}
	if len(resp.Filters.Status) != 2 || resp.Filters.Sync[0] != "OutOfSync" {
		t.Errorf("filters = %+v", *resp.Filters)
	}
	if resp.Filters.Matched != len(resp.Services) {
		t.Errorf("matched = %d, services = %d", resp.Filters.Matched, len(resp.Services))
	}
	for _, svc := range resp.Services {
		if svc.State != "DEGRADED" && svc.State != "DRIFT" {
			t.Errorf("unfiltered service leaked: %q", svc.State)
		}
	}
	// Counts describe the whole cluster, not the filtered slice.
	if resp.Summary.Total != 14 {
		t.Errorf("summary.total = %d, want 14", resp.Summary.Total)
	}

	plain := get(t, h, "/k8s-status/api/status").Body.String()
	if strings.Contains(plain, `"filters"`) {
		t.Error("filters object should be omitted when nothing is filtered")
	}
}

func TestHealthzIgnoresFilters(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})
	rec := get(t, h, "/k8s-status/healthz?status=BANANAS&refresh=0")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

// A stale colspan is the silent way a column change breaks, so it is asserted
// against the real header cell count rather than a hard-coded number.
func TestColspansMatchTheColumnCount(t *testing.T) {
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: fixtureSnapshot(t)})

	for _, path := range []string{"/k8s-status/", "/k8s-status/?status=BANANAS"} {
		body := get(t, h, path).Body.String()

		head := regexp.MustCompile(`(?s)<thead>.*?</thead>`).FindString(body)
		if head == "" {
			t.Fatalf("%s: no thead rendered", path)
		}
		columns := len(regexp.MustCompile(`<th[ >]`).FindAllString(head, -1))
		if columns != 6 {
			t.Errorf("%s: header cells = %d, want 6", path, columns)
		}
		for _, m := range regexp.MustCompile(`colspan="(\d+)"`).FindAllStringSubmatch(body, -1) {
			if m[1] != strconv.Itoa(columns) {
				t.Errorf("%s: colspan=%s, want %d", path, m[1], columns)
			}
		}
	}
}

func TestVersionColumnsAreSeparate(t *testing.T) {
	snap := fixtureSnapshot(t)
	snap.Services = append(snap.Services, status.Service{
		Name: "no-image-app", Version: "develop", Revision: "abcdef1234", State: status.StateOK, Sync: "Synced",
	})
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: snap})

	body := get(t, h, "/k8s-status/").Body.String()
	if !strings.Contains(body, "<th>App version</th>") || !strings.Contains(body, `<th class="hide-sm">Chart version</th>`) {
		t.Error("want two separate version headers, the chart one droppable on narrow screens")
	}
	if strings.Contains(body, "app / chart") {
		t.Error("the combined sub-label should be gone")
	}
	if !strings.Contains(body, `<td class="appver">`) || !strings.Contains(body, `<td class="chartver hide-sm"`) {
		t.Error("want one cell per version column")
	}
	// A service with no reported image renders an empty cell, not a broken row.
	if !strings.Contains(body, `<td class="appver"></td>`) {
		t.Error("want an empty App version cell for a service with no image")
	}
	if !strings.Contains(body, "no-image-app") {
		t.Error("the image-less service should still be listed")
	}
}

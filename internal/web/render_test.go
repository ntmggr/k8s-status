package web

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ntmggr/k8s-status/internal/status"
)

func tileLabels(tiles []Tile) []string {
	var out []string
	for _, t := range tiles {
		out = append(out, t.Label)
	}
	return out
}

// TestTilesOrder covers the fixed order Tiles() builds in, and that zero-count
// categories are skipped rather than rendered as empty tiles. Rendering is a single
// flex-wrap container (see index.html), so unlike the old two-row split there is no
// row-chunking left to test here: wrapping at any viewport width, and keeping the one
// truly leftover tile in the true last line, is CSS's job now, not Go's.
func TestTilesOrder(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{
		Total: 100, OK: 89, Degraded: 7, Progressing: 5, Drift: 30, Prune: 9, Suspended: 1,
	}}}
	got := tileLabels(d.Tiles())
	want := []string{"services", "degraded", "progressing", "drift", "prune", "suspended", "ok"}
	if !equalStrings(got, want) {
		t.Errorf("Tiles = %v, want %v", got, want)
	}
}

func TestTilesSkipsZeroCategories(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 5, OK: 5}}}
	got := tileLabels(d.Tiles())
	want := []string{"services", "ok"}
	if !equalStrings(got, want) {
		t.Errorf("Tiles = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAttentionItemsFiltersAndOrders(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Services: []status.Service{
		{Name: "accounts-api", Source: status.SourceArgoCD, State: status.StateOK, Detail: "all good"},
		{Name: "orders-api", Source: status.SourceArgoCD, State: status.StateDegraded, Detail: "0/2 replicas available"},
		{Name: "search-api", Source: status.SourceArgoCD, State: status.StateWarning, Detail: "health unknown"},
		{Name: "cache-shim", Namespace: "cache", Source: status.SourceFlux, State: status.StateOK,
			Blocked: &status.Blocked{Resources: []string{"cpu"}, Pods: 1}},
		{Name: "edge-proxy", Source: status.SourceArgoCD, State: status.StateProgressing, Detail: "rolling out"},
	}}}

	items := d.AttentionItems()
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3 (OK and PROGRESSING excluded)", len(items))
	}

	got := map[string]AttentionItem{}
	for _, it := range items {
		got[it.Name] = it
	}
	if _, ok := got["accounts-api"]; ok {
		t.Error("OK service must not appear in the digest")
	}
	if _, ok := got["edge-proxy"]; ok {
		t.Error("PROGRESSING service must not appear in the digest")
	}
	if it := got["orders-api"]; it.Reason != "0/2 replicas available" {
		t.Errorf("orders-api reason = %q, want the Detail text", it.Reason)
	}
	if it := got["cache-shim"]; it.Reason == "" || it.Anchor != "svc-flux-cache-cache-shim" {
		t.Errorf("cache-shim = %+v, want a Blocked.Reason() and a namespaced anchor", it)
	}
}

func TestAttentionItemsNilSnapshotIsNoop(t *testing.T) {
	d := pageData{}
	if got := d.AttentionItems(); got != nil {
		t.Errorf("AttentionItems() = %v, want nil", got)
	}
}

func TestTilesComputePercent(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{
		Total: 20, OK: 15, Degraded: 5,
	}}}
	tiles := d.Tiles()
	byLabel := map[string]Tile{}
	for _, ti := range tiles {
		byLabel[ti.Label] = ti
	}
	if got := byLabel["services"].Percent; got != 100 {
		t.Errorf("services Percent = %d, want 100", got)
	}
	if got := byLabel["degraded"].Percent; got != 25 {
		t.Errorf("degraded Percent = %d, want 25", got)
	}
	if got := byLabel["ok"].Percent; got != 75 {
		t.Errorf("ok Percent = %d, want 75", got)
	}
}

func TestRingSegmentsSplitByStateInOrder(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{
		Total: 20, OK: 15, Degraded: 5,
	}}}
	got := ringSegments(d.Tiles())
	want := []RingSegment{
		{Class: "t-ok", Dash: "74.20 25.80", Offset: "100.00"},
		{Class: "t-degraded", Dash: "24.20 75.80", Offset: "25.00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ringSegments() = %+v, want %+v", got, want)
	}
}

func TestRingSegmentsMatchHealthPercent(t *testing.T) {
	s := status.Summary{Total: 14, OK: 6, Progressing: 1, Drift: 1, Warning: 1, Degraded: 2, Prune: 2, Suspended: 1}
	d := pageData{Snapshot: &status.Snapshot{Summary: s}}
	got := ringSegments(d.Tiles())
	var classes []string
	for _, seg := range got {
		classes = append(classes, seg.Class)
	}
	if want := []string{"t-ok", "t-progressing", "t-drift", "t-warning", "t-degraded"}; !reflect.DeepEqual(classes, want) {
		t.Fatalf("segment order = %v, want %v", classes, want)
	}
	// The drift arc starts where OK + progressing (7 of 11, 63.64%) end.
	if got[2].Offset != "36.36" {
		t.Errorf("drift offset = %q, want 36.36 (100 - 63.64)", got[2].Offset)
	}
	if h := s.Health(); h.Percent != 64 {
		t.Fatalf("Health().Percent = %d, want 64", h.Percent)
	}
}

func TestRingSegmentsSingleStateHasNoGap(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 3, OK: 3}}}
	got := ringSegments(d.Tiles())
	if len(got) != 1 || got[0].Dash != "100.00 0.00" {
		t.Errorf("ringSegments() = %+v, want one full arc", got)
	}
}

func TestRingSegmentsEmptyWithoutCountedStates(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 3, Prune: 2, Suspended: 1}}}
	if got := ringSegments(d.Tiles()); got != nil {
		t.Errorf("ringSegments() = %+v, want nil when only excluded states exist", got)
	}
	if got := ringSegments(nil); got != nil {
		t.Errorf("ringSegments(nil) = %+v, want nil", got)
	}
}
func TestGaugeClassUsesSharedHealthThresholds(t *testing.T) {
	for _, tc := range []struct {
		known   bool
		percent int
		want    string
	}{
		{false, 100, "is-unknown"},
		{true, 90, "is-ok"},
		{true, 75, "is-warn"},
		{true, 74, "is-bad"},
	} {
		if got := gaugeClass(tc.known, tc.percent); got != tc.want {
			t.Errorf("gaugeClass(%v, %d) = %q, want %q", tc.known, tc.percent, got, tc.want)
		}
	}
}

func TestGPUAllocationAPIKeepsMissingDataUnknown(t *testing.T) {
	got := gpuAlloc(status.Service{GPU: true})
	if got.Known || got.Measured || got.State != "unknown" {
		t.Fatalf("unknown GPU allocation = %+v", got)
	}
}

func TestArchClassMapsKnownAndUnknownValues(t *testing.T) {
	for _, tc := range []struct{ arch, want string }{
		{"arm64", "arm64"}, {"amd64", "amd64"}, {"s390x", "other"}, {"unknown", "other"},
	} {
		if got := archClass(tc.arch); got != tc.want {
			t.Errorf("archClass(%q) = %q, want %q", tc.arch, got, tc.want)
		}
	}
}

func TestDashboardRendersEveryArchitectureAndStrictPolicy(t *testing.T) {
	snap := fixtureSnapshot(t)
	snap.Nodes = &status.NodeStats{Total: 4, CPUNodes: 4, Arch: []status.ArchCount{
		{Arch: "arm64", Count: 2}, {Arch: "s390x", Count: 1}, {Arch: "unknown", Count: 1},
	}}
	snap.Mesh = &status.MeshSection{Installed: true, Effective: status.MeshStrict}
	snap.Summary.MeshPolicyEligible = 3
	snap.Summary.MeshPolicyPermissive = 1
	snap.Summary.MeshPolicyDisabled = 1
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: snap})
	body := get(t, h, "/k8s-status/?status=OK&refresh=60").Body.String()
	for _, want := range []string{
		"1 s390x", "1 unknown", "33% enforce STRICT mTLS",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	for _, want := range []string{
		"1 service with mTLS disabled",
		`href="/k8s-status/?refresh=60#svc-argocd-media-encoder"`,
		`href="#main"`, `<main id="main">`,
		`tabindex="0" role="region" aria-label="Services table"`,
		`<span>Degraded</span><b>2</b>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(body, "67% enforce STRICT") {
		t.Error("disabled policy was incorrectly counted as strict")
	}
}

func TestDashboardNavigationAndDetailRows(t *testing.T) {
	snap := fixtureSnapshot(t)
	snap.Nodes = &status.NodeStats{Total: 1, CPUNodes: 1}
	h := newTestServer(t, Config{BasePath: "/k8s-status"}, fakeProvider{snap: snap})
	body := get(t, h, "/k8s-status/?refresh=0").Body.String()
	for _, target := range []string{"overview", "cluster", "attention", "services"} {
		if !strings.Contains(body, `href="#`+target+`"`) ||
			strings.Count(body, `id="`+target+`"`) != 1 {
			t.Errorf("navigation target %q must exist exactly once", target)
		}
	}
	for _, want := range []string{`class="donut-seg t-ok"`, `class="kpi"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %s", want)
		}
	}
	previous := -1
	for _, marker := range []string{
		`<div class="kpis">`,
		`<div class="insights">`,
		`class="split-row split-row-details"`,
		`class="capacity" id="cluster"`,
		`id="attention"`,
		`id="services"`,
	} {
		position := strings.Index(body, marker)
		if position <= previous {
			t.Fatalf("dashboard section %q missing or out of order", marker)
		}
		previous = position
	}
}

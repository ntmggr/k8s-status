package web

import (
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

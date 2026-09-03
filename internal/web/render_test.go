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

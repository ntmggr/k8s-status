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

// TestTileRowSplitKeepsOKPaired is the case a real cluster hit: with only "services",
// one attention tile (e.g. drift) and "ok" nonzero, the old ceil-on-top split left "ok"
// stranded alone on the bottom row, reading like a leftover rather than a group. The
// extra tile must go to the bottom instead, since "services" (always first) reads fine
// alone as a header count but "ok" (always last) is a peer of the attention tiles.
func TestTileRowSplitKeepsOKPaired(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 60, OK: 59, Drift: 1}}}

	top := tileLabels(d.TileRowTop())
	bottom := tileLabels(d.TileRowBottom())

	if want := []string{"services"}; !equalStrings(top, want) {
		t.Errorf("TileRowTop = %v, want %v", top, want)
	}
	if want := []string{"drift", "ok"}; !equalStrings(bottom, want) {
		t.Errorf("TileRowBottom = %v, want %v (ok must not be alone)", bottom, want)
	}
}

func TestTileRowSplitBalancesEvenCount(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 10, OK: 8, Degraded: 1, Warning: 1}}}

	top := tileLabels(d.TileRowTop())
	bottom := tileLabels(d.TileRowBottom())

	if want := []string{"services", "degraded"}; !equalStrings(top, want) {
		t.Errorf("TileRowTop = %v, want %v", top, want)
	}
	if want := []string{"warning", "ok"}; !equalStrings(bottom, want) {
		t.Errorf("TileRowBottom = %v, want %v", bottom, want)
	}
}

func TestTileRowSplitNoAttentionTiles(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 5, OK: 5}}}

	top := tileLabels(d.TileRowTop())
	bottom := tileLabels(d.TileRowBottom())

	if want := []string{"services"}; !equalStrings(top, want) {
		t.Errorf("TileRowTop = %v, want %v", top, want)
	}
	if want := []string{"ok"}; !equalStrings(bottom, want) {
		t.Errorf("TileRowBottom = %v, want %v", bottom, want)
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

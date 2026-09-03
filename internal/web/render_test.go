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

func rowLabels(rows [][]Tile) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = tileLabels(r)
	}
	return out
}

func equalRows(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalStrings(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestTileRowsOddCountTrailsLastRow(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 60, OK: 59, Drift: 1}}}
	got := rowLabels(d.TileRows())
	want := [][]string{{"services", "drift"}, {"ok"}}
	if !equalRows(got, want) {
		t.Errorf("TileRows = %v, want %v", got, want)
	}
}

func TestTileRowsEvenCount(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 10, OK: 8, Degraded: 1, Warning: 1}}}
	got := rowLabels(d.TileRows())
	want := [][]string{{"services", "degraded"}, {"warning", "ok"}}
	if !equalRows(got, want) {
		t.Errorf("TileRows = %v, want %v", got, want)
	}
}

func TestTileRowsNoAttentionTiles(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{Total: 5, OK: 5}}}
	got := rowLabels(d.TileRows())
	want := [][]string{{"services", "ok"}}
	if !equalRows(got, want) {
		t.Errorf("TileRows = %v, want %v", got, want)
	}
}

// TestTileRowsManyTilesNeverExceedsRowCapacity is the case a real cluster hit: with
// enough nonzero categories to need more than two rows, the old top/bottom split by
// count (not by physical capacity) put 3+ tiles in a single .tiles flex container,
// which then wrapped internally and stranded the extra tile mid-page instead of at
// the true bottom of the stack. Every row here must hold at most tilesPerRow tiles,
// and the one truly leftover tile must be alone in the last row, not the third row.
func TestTileRowsManyTilesNeverExceedsRowCapacity(t *testing.T) {
	d := pageData{Snapshot: &status.Snapshot{Summary: status.Summary{
		Total: 100, OK: 89, Degraded: 7, Progressing: 5, Drift: 30, Prune: 9, Suspended: 1,
	}}}
	got := d.TileRows()
	for i, row := range got {
		if len(row) > tilesPerRow {
			t.Errorf("row %d has %d tiles, want at most %d: %v", i, len(row), tilesPerRow, tileLabels(row))
		}
	}
	want := [][]string{
		{"services", "degraded"},
		{"progressing", "drift"},
		{"prune", "suspended"},
		{"ok"},
	}
	if got := rowLabels(got); !equalRows(got, want) {
		t.Errorf("TileRows = %v, want %v", got, want)
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

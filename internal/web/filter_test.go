package web

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ntmggr/k8s-status/internal/status"
)

func query(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	return v
}

func TestParseFilter(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  Filter
		empty bool
	}{
		{name: "repeated", raw: "status=DEGRADED&status=DRIFT", want: Filter{Status: []string{"DEGRADED", "DRIFT"}}},
		{name: "comma separated", raw: "status=DEGRADED,DRIFT", want: Filter{Status: []string{"DEGRADED", "DRIFT"}}},
		{name: "mixed forms", raw: "status=DEGRADED,DRIFT&status=PRUNE", want: Filter{Status: []string{"DEGRADED", "DRIFT", "PRUNE"}}},
		{name: "mixed case kept for the chip", raw: "status=degraded", want: Filter{Status: []string{"degraded"}}},
		{name: "whitespace trimmed", raw: "status=%20DEGRADED%20,%20DRIFT", want: Filter{Status: []string{"DEGRADED", "DRIFT"}}},
		{name: "case-insensitive dedupe", raw: "status=DEGRADED&status=degraded", want: Filter{Status: []string{"DEGRADED"}}},
		{name: "empty values dropped", raw: "status=&status=,&sync=", want: Filter{}, empty: true},
		{name: "unknown value kept", raw: "status=BANANAS", want: Filter{Status: []string{"BANANAS"}}},
		{name: "sync", raw: "sync=OutOfSync", want: Filter{Sync: []string{"OutOfSync"}}},
		{name: "gpu", raw: "gpu=true", want: Filter{GPU: "true"}},
		{name: "combined", raw: "status=DEGRADED,DRIFT&sync=OutOfSync&gpu=false",
			want: Filter{Status: []string{"DEGRADED", "DRIFT"}, Sync: []string{"OutOfSync"}, GPU: "false"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseFilter(query(t, tc.raw))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseFilter(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
			if got.Active() == tc.empty && tc.empty {
				t.Errorf("Active() = %t for %q", got.Active(), tc.raw)
			}
		})
	}
}

func testServices() []status.Service {
	return []status.Service{
		{Name: "a", State: status.StateDegraded, Sync: "OutOfSync", GPU: true},
		{Name: "b", State: status.StateDegraded, Sync: "Synced"},
		{Name: "c", State: status.StateDrift, Sync: "OutOfSync"},
		{Name: "d", State: status.StateOK, Sync: "Synced", GPU: true},
		{Name: "e", State: status.StateOK, Sync: ""},
	}
}

func names(svcs []status.Service) []string {
	out := make([]string, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.Name)
	}
	return out
}

func TestFilterApply(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{raw: "", want: []string{"a", "b", "c", "d", "e"}},
		{raw: "status=DEGRADED", want: []string{"a", "b"}},
		{raw: "status=degraded", want: []string{"a", "b"}},
		{raw: "status=DEGRADED,DRIFT", want: []string{"a", "b", "c"}},
		{raw: "status=DEGRADED&status=DRIFT", want: []string{"a", "b", "c"}},
		{raw: "sync=OutOfSync", want: []string{"a", "c"}},
		{raw: "sync=Unknown", want: []string{"e"}},
		{raw: "gpu=true", want: []string{"a", "d"}},
		{raw: "gpu=false", want: []string{"b", "c", "e"}},
		// AND across parameters, OR within one.
		{raw: "status=DEGRADED,DRIFT&gpu=true", want: []string{"a"}},
		{raw: "status=DEGRADED&sync=Synced", want: []string{"b"}},
		{raw: "status=OK&sync=Synced&gpu=true", want: []string{"d"}},
		// Unrecognised values select nothing rather than everything.
		{raw: "status=BANANAS", want: []string{}},
		{raw: "gpu=banana", want: []string{}},
		{raw: "status=DEGRADED&sync=BANANAS", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := names(ParseFilter(query(t, tc.raw)).Apply(testServices()))
			if len(got) != len(tc.want) {
				t.Fatalf("%q selected %v, want %v", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("%q selected %v, want %v", tc.raw, got, tc.want)
				}
			}
		})
	}
}

func TestRefreshSeconds(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{raw: "", want: 30},
		{raw: "refresh=", want: 30},
		{raw: "refresh=%20%20", want: 30},
		{raw: "refresh=abc", want: 30},
		{raw: "refresh=-5", want: 30},
		{raw: "refresh=60", want: 60},
		{raw: "refresh=0", want: 0},
		{raw: "refresh=1", want: minRefreshSeconds},
		{raw: "refresh=999999", want: maxRefreshSeconds},
	}
	for _, tc := range cases {
		if got := refreshSeconds(query(t, tc.raw), 30); got != tc.want {
			t.Errorf("refreshSeconds(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestFilterHrefTogglesAndPreserves(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		kind, val  string
		wantSuffix string
	}{
		{name: "adds", raw: "", kind: "status", val: "DEGRADED", wantSuffix: "/k8s-status/?status=DEGRADED"},
		{name: "clears when active", raw: "status=DEGRADED", kind: "status", val: "DEGRADED", wantSuffix: "/k8s-status/"},
		{name: "clears case-insensitively", raw: "status=degraded", kind: "status", val: "DEGRADED", wantSuffix: "/k8s-status/"},
		{name: "keeps siblings", raw: "status=DEGRADED&status=DRIFT", kind: "status", val: "DRIFT", wantSuffix: "/k8s-status/?status=DEGRADED"},
		{name: "keeps refresh", raw: "refresh=60", kind: "status", val: "DEGRADED", wantSuffix: "/k8s-status/?refresh=60&status=DEGRADED"},
		{name: "keeps refresh while clearing", raw: "status=DEGRADED&refresh=60", kind: "status", val: "DEGRADED", wantSuffix: "/k8s-status/?refresh=60"},
		{name: "gpu toggle on", raw: "", kind: "gpu", val: "true", wantSuffix: "/k8s-status/?gpu=true"},
		{name: "gpu toggle off", raw: "gpu=true", kind: "gpu", val: "true", wantSuffix: "/k8s-status/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := query(t, tc.raw)
			d := pageData{BasePath: "/k8s-status", Query: q, Filter: ParseFilter(q)}
			if got := d.FilterHref(tc.kind, tc.val); got != tc.wantSuffix {
				t.Errorf("FilterHref(%q, %q) with %q = %q, want %q", tc.kind, tc.val, tc.raw, got, tc.wantSuffix)
			}
		})
	}
}

func TestClearHrefKeepsRefresh(t *testing.T) {
	q := query(t, "status=DEGRADED&sync=OutOfSync&gpu=true&refresh=60")
	d := pageData{BasePath: "/k8s-status", Query: q, Filter: ParseFilter(q)}
	if got, want := d.ClearHref(), "/k8s-status/?refresh=60"; got != want {
		t.Errorf("ClearHref = %q, want %q", got, want)
	}
}

func TestChipsRemoveOneFilterEach(t *testing.T) {
	q := query(t, "status=DEGRADED,DRIFT&gpu=true")
	d := pageData{BasePath: "/k8s-status", Query: q, Filter: ParseFilter(q)}

	chips := d.Chips()
	if len(chips) != 3 {
		t.Fatalf("chips = %+v, want 3", chips)
	}
	want := []Chip{
		{Label: "status DEGRADED", RemoveHref: "/k8s-status/?gpu=true&status=DRIFT"},
		{Label: "status DRIFT", RemoveHref: "/k8s-status/?gpu=true&status=DEGRADED"},
		{Label: "gpu true", RemoveHref: "/k8s-status/?status=DEGRADED&status=DRIFT"},
	}
	if !reflect.DeepEqual(chips, want) {
		t.Errorf("chips = %+v, want %+v", chips, want)
	}
}

func TestRefreshOptionsKeepFilters(t *testing.T) {
	q := query(t, "status=DEGRADED&refresh=60")
	d := pageData{BasePath: "/k8s-status", Query: q, Filter: ParseFilter(q), RefreshSeconds: 60}

	opts := d.RefreshOptions()
	if len(opts) != len(refreshChoices) {
		t.Fatalf("options = %d, want %d", len(opts), len(refreshChoices))
	}
	var active int
	for _, o := range opts {
		if !strings.Contains(o.Href, "status=DEGRADED") {
			t.Errorf("refresh link %q dropped the active filter", o.Href)
		}
		if o.Active {
			active++
			if o.Label != "60s" {
				t.Errorf("active option = %q, want 60s", o.Label)
			}
		}
	}
	if active != 1 {
		t.Errorf("active options = %d, want 1", active)
	}
	if opts[len(opts)-1].Label != "off" || !strings.Contains(opts[len(opts)-1].Href, "refresh=0") {
		t.Errorf("last option = %+v, want the off switch", opts[len(opts)-1])
	}
}

// The views chips select a section; the status tiles select rows inside the services
// table. Choosing one must visibly deselect the other, or the page shows a state the
// user cannot account for.
func TestViewAndRowFiltersAreMutuallyExclusive(t *testing.T) {
	href := func(raw, kind, val string) string {
		q := query(t, raw)
		d := pageData{BasePath: "/k8s-status", Query: q, Filter: ParseFilter(q)}
		return d.FilterHref(kind, val)
	}

	// Choosing a row filter drops the section view.
	if got := href("view=unmanaged", "gpu", "true"); strings.Contains(got, "view=") {
		t.Errorf("selecting gpu kept the view: %s", got)
	}
	if got := href("view=unmanaged", "status", "DEGRADED"); strings.Contains(got, "view=") {
		t.Errorf("selecting a status kept the view: %s", got)
	}
	// Choosing the section view drops the row filters.
	got := href("gpu=true&status=DEGRADED", "view", "unmanaged")
	if strings.Contains(got, "gpu=") || strings.Contains(got, "status=") {
		t.Errorf("selecting the view kept row filters: %s", got)
	}
	if !strings.Contains(got, "view=unmanaged") {
		t.Errorf("view was not applied: %s", got)
	}
	// It still toggles off.
	if got := href("view=unmanaged", "view", "unmanaged"); strings.Contains(got, "view=") {
		t.Errorf("clicking the active view should clear it: %s", got)
	}
}

// Without this the chip never lights up, because has() did not know the kind.
func TestViewFilterReportsItselfActive(t *testing.T) {
	q := query(t, "view=unmanaged")
	d := pageData{BasePath: "/k8s-status", Query: q, Filter: ParseFilter(q)}
	if !d.FilterActive("view", "unmanaged") {
		t.Error("the not-in-gitops chip must render as selected when the view is active")
	}
	if d.FilterActive("view", "something-else") {
		t.Error("a different view value must not report active")
	}
}

// A hand-written URL can carry both; the page must still render one coherent state.
func TestViewWinsOverRowFiltersInAHandWrittenURL(t *testing.T) {
	f := ParseFilter(query(t, "view=unmanaged&gpu=true&status=DEGRADED&sync=Synced"))
	if f.View != "unmanaged" {
		t.Fatalf("view = %q, want unmanaged", f.View)
	}
	if f.GPU != "" || len(f.Status) != 0 || len(f.Sync) != 0 {
		t.Errorf("row filters survived alongside a view: gpu=%q status=%v sync=%v", f.GPU, f.Status, f.Sync)
	}
	if f.Active() {
		t.Error("with only a view set, no row filter should report active")
	}
}

// Clicking the services tile means "show me everything". It cleared only the filters
// that existed when it was written, so selecting an architecture and then clicking it
// left the page still narrowed.
func TestClearHrefDropsEveryFilter(t *testing.T) {
	for _, q := range []string{
		"status=DEGRADED", "sync=OutOfSync", "gpu=true",
		"arch=amd64", "blocked=cpu", "view=unmanaged",
		"arch=arm64&status=OK&blocked=placement",
	} {
		u, err := url.Parse("/k8s-status/?" + q)
		if err != nil {
			t.Fatal(err)
		}
		d := pageData{BasePath: "/k8s-status", Filter: ParseFilter(u.Query())}
		got := d.ClearHref()
		cleared, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if f := ParseFilter(cleared.Query()); f.Active() || f.View != "" {
			t.Errorf("ClearHref on %q gave %q, which is still filtered", q, got)
		}
	}
}

// Every active filter has to appear as a removable chip, or it cannot be undone
// without editing the URL.
func TestChipsCoverEveryFilter(t *testing.T) {
	u, _ := url.Parse("/k8s-status/?gpu=true&arch=amd64&blocked=cpu&status=OK")
	d := pageData{BasePath: "/k8s-status", Filter: ParseFilter(u.Query())}
	// gpu and arch and blocked are one exclusive group in the UI, but ParseFilter
	// accepts them together, so all four should be listed.
	if n := len(d.Chips()); n != 4 {
		labels := []string{}
		for _, c := range d.Chips() {
			labels = append(labels, c.Label)
		}
		t.Fatalf("got %d chips %v, want one per active filter", n, labels)
	}
}

func testWorkloads() []status.Workload {
	return []status.Workload{
		{Name: "single-zone", Zones: status.ZoneSpread{Zones: []string{"a"}, Pods: 1}, Nodes: status.NodeSpread{Nodes: []string{"n1"}, Pods: 1}},
		{Name: "multi-zone", Zones: status.ZoneSpread{Zones: []string{"a", "b"}, Pods: 2}, Nodes: status.NodeSpread{Nodes: []string{"n1", "n2"}, Pods: 2}},
		{Name: "no-answer"}, // Zones/Nodes zero-value: Known() is false
	}
}

func workloadNames(ws []status.Workload) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Name)
	}
	return out
}

func TestApplyWorkloadsFiltersBySpreadOnly(t *testing.T) {
	items := testWorkloads()
	cases := []struct {
		spread string
		want   []string
	}{
		{spread: "", want: []string{"single-zone", "multi-zone", "no-answer"}},
		{spread: "zone", want: []string{"single-zone"}},
		{spread: "node", want: []string{"single-zone"}},
		{spread: "bogus", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.spread, func(t *testing.T) {
			f := Filter{Spread: tc.spread}
			got := workloadNames(f.ApplyWorkloads(items))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ApplyWorkloads(spread=%q) = %v, want %v", tc.spread, got, tc.want)
			}
		})
	}
}

// A row filter (status, gpu, ...) still hides the not-in-gitops table -- it has no
// comparable concept, so a second table would be noise. Spread is the one exception:
// see ShowUnmanaged's own comment for why.
func TestShowUnmanagedSpreadIsAnException(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{name: "no filter", f: Filter{}, want: true},
		{name: "spread only", f: Filter{Spread: "zone"}, want: true},
		{name: "status filter hides it", f: Filter{Status: []string{"DEGRADED"}}, want: false},
		{name: "spread alongside status still shows it", f: Filter{Spread: "node", Status: []string{"DEGRADED"}}, want: true},
		{name: "view=unmanaged always shows it", f: Filter{View: "unmanaged"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.ShowUnmanaged(); got != tc.want {
				t.Errorf("ShowUnmanaged() = %t, want %t", got, tc.want)
			}
		})
	}
}

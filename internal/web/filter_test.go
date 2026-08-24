package web

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ntmggr/srv-status/internal/status"
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
		{name: "empty values dropped", raw: "status=&status=,,&sync=", want: Filter{}, empty: true},
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
		{name: "adds", raw: "", kind: "status", val: "DEGRADED", wantSuffix: "/srv-status/?status=DEGRADED"},
		{name: "clears when active", raw: "status=DEGRADED", kind: "status", val: "DEGRADED", wantSuffix: "/srv-status/"},
		{name: "clears case-insensitively", raw: "status=degraded", kind: "status", val: "DEGRADED", wantSuffix: "/srv-status/"},
		{name: "keeps siblings", raw: "status=DEGRADED&status=DRIFT", kind: "status", val: "DRIFT", wantSuffix: "/srv-status/?status=DEGRADED"},
		{name: "keeps refresh", raw: "refresh=60", kind: "status", val: "DEGRADED", wantSuffix: "/srv-status/?refresh=60&status=DEGRADED"},
		{name: "keeps refresh while clearing", raw: "status=DEGRADED&refresh=60", kind: "status", val: "DEGRADED", wantSuffix: "/srv-status/?refresh=60"},
		{name: "gpu toggle on", raw: "", kind: "gpu", val: "true", wantSuffix: "/srv-status/?gpu=true"},
		{name: "gpu toggle off", raw: "gpu=true", kind: "gpu", val: "true", wantSuffix: "/srv-status/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := query(t, tc.raw)
			d := pageData{BasePath: "/srv-status", Query: q, Filter: ParseFilter(q)}
			if got := d.FilterHref(tc.kind, tc.val); got != tc.wantSuffix {
				t.Errorf("FilterHref(%q, %q) with %q = %q, want %q", tc.kind, tc.val, tc.raw, got, tc.wantSuffix)
			}
		})
	}
}

func TestClearHrefKeepsRefresh(t *testing.T) {
	q := query(t, "status=DEGRADED&sync=OutOfSync&gpu=true&refresh=60")
	d := pageData{BasePath: "/srv-status", Query: q, Filter: ParseFilter(q)}
	if got, want := d.ClearHref(), "/srv-status/?refresh=60"; got != want {
		t.Errorf("ClearHref = %q, want %q", got, want)
	}
}

func TestChipsRemoveOneFilterEach(t *testing.T) {
	q := query(t, "status=DEGRADED,DRIFT&gpu=true")
	d := pageData{BasePath: "/srv-status", Query: q, Filter: ParseFilter(q)}

	chips := d.Chips()
	if len(chips) != 3 {
		t.Fatalf("chips = %+v, want 3", chips)
	}
	want := []Chip{
		{Label: "status DEGRADED", RemoveHref: "/srv-status/?gpu=true&status=DRIFT"},
		{Label: "status DRIFT", RemoveHref: "/srv-status/?gpu=true&status=DEGRADED"},
		{Label: "gpu true", RemoveHref: "/srv-status/?status=DEGRADED&status=DRIFT"},
	}
	if !reflect.DeepEqual(chips, want) {
		t.Errorf("chips = %+v, want %+v", chips, want)
	}
}

func TestRefreshOptionsKeepFilters(t *testing.T) {
	q := query(t, "status=DEGRADED&refresh=60")
	d := pageData{BasePath: "/srv-status", Query: q, Filter: ParseFilter(q), RefreshSeconds: 60}

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

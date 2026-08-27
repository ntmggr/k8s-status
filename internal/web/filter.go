package web

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/ntmggr/k8s-status/internal/status"
)

const (
	filterStatus  = "status"
	filterSync    = "sync"
	filterGPU     = "gpu"
	filterBlocked = "blocked"
	filterArch    = "arch"
	filterView    = "view"
)

// Filter is the row selection parsed from the query string. Values are kept as the
// user spelled them so a chip can echo them back; matching is case-insensitive.
// An unrecognised value matches nothing rather than being dropped, so a typo shows an
// empty table instead of silently showing everything.
type Filter struct {
	Status  []string
	Sync    []string
	GPU     string
	Blocked string
	Arch    string
	// View narrows the page to one section rather than filtering rows.
	// "unmanaged" shows only workloads ArgoCD does not manage.
	View string
}

// ParseFilter accepts both repeated parameters and comma-separated values.
func ParseFilter(q url.Values) Filter {
	f := Filter{
		Status:  parseFilterList(q[filterStatus]),
		Sync:    parseFilterList(q[filterSync]),
		GPU:     strings.TrimSpace(q.Get(filterGPU)),
		Blocked: strings.TrimSpace(q.Get(filterBlocked)),
		Arch:    strings.TrimSpace(q.Get(filterArch)),
		View:    strings.ToLower(strings.TrimSpace(q.Get(filterView))),
	}
	// A section view and the row filters describe different tables. The links never
	// produce both, but a hand-written URL can, and rendering half of each state is
	// worse than picking one. The view wins because it is the coarser choice.
	if f.View != "" {
		f.Status, f.Sync, f.GPU, f.Blocked, f.Arch = nil, nil, "", "", ""
	}
	return f
}

func parseFilterList(vals []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := strings.ToLower(part)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, part)
		}
	}
	return out
}

func (f Filter) Active() bool {
	return len(f.Status) > 0 || len(f.Sync) > 0 || f.GPU != "" || f.Blocked != "" || f.Arch != ""
}

func (f Filter) list(kind string) []string {
	switch kind {
	case filterStatus:
		return f.Status
	case filterSync:
		return f.Sync
	}
	return nil
}

func (f Filter) has(kind, value string) bool {
	switch kind {
	case filterStatus:
		return containsFold(f.Status, value)
	case filterSync:
		return containsFold(f.Sync, value)
	case filterGPU:
		return strings.EqualFold(f.GPU, value)
	case filterBlocked:
		return strings.EqualFold(f.Blocked, value)
	case filterArch:
		return strings.EqualFold(f.Arch, value)
	case filterView:
		return strings.EqualFold(f.View, value)
	}
	return false
}

// gpuWant reports the requested GPU flag; ok is false for an unparseable value,
// which then matches no rows.
func (f Filter) gpuWant() (want, ok bool) {
	if f.GPU == "" {
		return false, false
	}
	b, err := strconv.ParseBool(f.GPU)
	if err != nil {
		return false, false
	}
	return b, true
}

func (f Filter) matches(svc status.Service) bool {
	if len(f.Status) > 0 && !containsFold(f.Status, string(svc.State)) {
		return false
	}
	if len(f.Sync) > 0 && !containsFold(f.Sync, syncOf(svc)) {
		return false
	}
	if f.Arch != "" && !strings.EqualFold(svc.Arch, f.Arch) {
		return false
	}
	if f.Blocked != "" {
		// "true" keeps meaning any shortage; a kind narrows it to one.
		if want, err := strconv.ParseBool(f.Blocked); err == nil {
			if (svc.Blocked != nil) != want {
				return false
			}
		} else if svc.Blocked == nil || !strings.EqualFold(svc.Blocked.Kind(), f.Blocked) {
			return false
		}
	}
	if f.GPU != "" {
		want, ok := f.gpuWant()
		if !ok || svc.GPU != want {
			return false
		}
	}
	return true
}

// Apply returns the rows the filter selects. Counts are deliberately not touched:
// see the note on the KPI tiles in the template.
func (f Filter) Apply(services []status.Service) []status.Service {
	if !f.Active() {
		return services
	}
	out := make([]status.Service, 0, len(services))
	for _, svc := range services {
		if f.matches(svc) {
			out = append(out, svc)
		}
	}
	return out
}

// syncOf normalizes a missing sync status so ?sync=Unknown can select those rows.
func syncOf(svc status.Service) string {
	if strings.TrimSpace(svc.Sync) == "" {
		return "Unknown"
	}
	return svc.Sync
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func appendFold(list []string, v string) []string {
	if containsFold(list, v) {
		return list
	}
	out := make([]string, len(list), len(list)+1)
	copy(out, list)
	return append(out, v)
}

func removeFold(list []string, v string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !strings.EqualFold(s, v) {
			out = append(out, s)
		}
	}
	return out
}

// filterHref renders a link to the page with the given selection applied.
func filterHref(basePath string, v url.Values) string {
	href := basePath + "/"
	if len(v) == 0 {
		return href
	}
	return href + "?" + v.Encode()
}

const (
	paramRefresh = "refresh"
	// A viewer can pick the meta-refresh interval, so it is clamped: the snapshot cache
	// absorbs most of the load, but nothing should be able to ask for a 1s page loop.
	minRefreshSeconds = 5
	maxRefreshSeconds = 3600
)

// refreshSeconds reads ?refresh=, falling back to the configured default for anything
// unparseable or negative. Zero means the viewer turned auto-refresh off.
func refreshSeconds(q url.Values, def int) int {
	raw := strings.TrimSpace(q.Get(paramRefresh))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	switch {
	case n == 0:
		return 0
	case n < minRefreshSeconds:
		return minRefreshSeconds
	case n > maxRefreshSeconds:
		return maxRefreshSeconds
	default:
		return n
	}
}

// ShowServices reports whether the ArgoCD/Flux services table belongs on the page.
func (f Filter) ShowServices() bool { return f.View != "unmanaged" }

// ShowUnmanaged reports whether the unmanaged workloads section belongs on the page.
// It is hidden while a service filter is active: if you asked to see only DEGRADED
// services, a second table of things that are not services at all is noise.
func (f Filter) ShowUnmanaged() bool {
	if f.View == "unmanaged" {
		return true
	}
	return f.View == "" && !f.Active()
}

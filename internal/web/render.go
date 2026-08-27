package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ntmggr/k8s-status/internal/status"
)

//go:embed templates/*
var templatesFS embed.FS

func parseTemplates() (*template.Template, error) {
	return template.New("index.html").Funcs(templateFuncs()).ParseFS(templatesFS, "templates/*.html")
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"short":      shortSHA,
		"lower":      strings.ToLower,
		"trunc":      truncate,
		"relTime":    relTime,
		"appURL":     func(base, name string) string { return strings.TrimRight(base, "/") + "/applications/" + name },
		"repoTree":   repoTreeURL,
		"repoCommit": repoCommitURL,
		"glyph":      stateGlyph,
		"clip":       clip,
		"icon":       icon,
		"brand":      brand,
	}
}

func shortSHA(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func relTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return humanize(time.Since(t))
}

func humanize(d time.Duration) string {
	if d < 45*time.Second {
		return "just now"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

type pageData struct {
	EnvName        string
	EnvType        string
	Region         string
	ClusterName    string
	BasePath       string
	ArgoCDUIBase   string
	RefreshSeconds int
	BuildVersion   string

	Snapshot *status.Snapshot
	// MultiSource is true when more than one GitOps controller is enabled. The Source
	// column only appears then, so a single-source cluster keeps the current table.
	MultiSource bool
	// ShowRootLine gates the environment header, every field of which comes from the
	// ArgoCD root Application. A Flux-only cluster has no root app, so the line is
	// omitted rather than rendered as a row of blanks.
	ShowRootLine bool
	Services     []status.Service
	Filter       Filter
	Query        url.Values
	Shown        int
	AgeSeconds   int
	Stale        bool
	LocalMode    bool
	Error        string
	ClusterError *ClusterError
	HasData      bool
}

// Chip is one active filter, with the link that removes just that one.
type Chip struct {
	Label      string
	RemoveHref string
}

// RefreshOption is one entry of the auto-refresh picker.
type RefreshOption struct {
	Label  string
	Href   string
	Active bool
}

// refreshChoices are the intervals offered in the header; 0 means off.
var refreshChoices = []int{10, 30, 60, 0}

// href renders the page URL for a modified copy of the current query, so every link
// preserves the parameters it is not itself changing.
func (d pageData) href(q url.Values) string {
	return filterHref(d.BasePath, q)
}

// query returns a mutable copy of the request's query string with the filter
// parameters rewritten in canonical form, so every generated link looks the same
// whether the viewer arrived via a comma-separated or a repeated parameter.
func (d pageData) query() url.Values {
	out := url.Values{}
	for k, vs := range d.Query {
		switch k {
		case filterStatus, filterSync, filterGPU:
		default:
			out[k] = append([]string(nil), vs...)
		}
	}
	setList(out, filterStatus, d.Filter.Status)
	setList(out, filterSync, d.Filter.Sync)
	if d.Filter.GPU != "" {
		out.Set(filterGPU, d.Filter.GPU)
	}
	return out
}

func setList(q url.Values, key string, values []string) {
	q.Del(key)
	for _, v := range values {
		q.Add(key, v)
	}
}

// allFilters is every parameter that narrows the page. Clearing, the chip row and the
// active check all read this one list. They each used to spell it out separately and
// drifted apart: "services" stopped clearing an architecture because that filter was
// added in one place and not the other two.
var allFilters = []string{filterStatus, filterSync, filterGPU, filterArch, filterBlocked, filterView}

// viewChips are the single-value chips in the views row. Exactly one can be active.
var viewChips = []string{filterView, filterGPU, filterArch, filterBlocked}

func isViewChip(kind string) bool {
	for _, k := range viewChips {
		if k == kind {
			return true
		}
	}
	return false
}

// FilterHref toggles one selection: clicking an active tile clears it, clicking an
// inactive one adds it. Everything else in the query, refresh included, is preserved.
func (d pageData) FilterHref(kind, value string) string {
	q := d.query()
	// The chips in the views row answer different questions about the same table, and
	// stacking them produced intersections nobody asked for: picking an architecture
	// while "on gpu" was active quietly showed only GPU services of that architecture.
	// They behave as one exclusive group, so a click always means "show me this".
	if isViewChip(kind) {
		if strings.EqualFold(q.Get(kind), value) {
			q.Del(kind) // clicking the active chip clears it
			return d.href(q)
		}
		for _, k := range viewChips {
			q.Del(k)
		}
		q.Set(kind, value)
		if kind == filterView {
			// A different section entirely, so the row filters go too.
			q.Del(filterStatus)
			q.Del(filterSync)
		}
		return d.href(q)
	}
	// Selecting a status or sync value is likewise a services-table action.
	q.Del(filterView)
	list := d.Filter.list(kind)
	if containsFold(list, value) {
		list = removeFold(list, value)
	} else {
		list = appendFold(list, value)
	}
	setList(q, kind, list)
	return d.href(q)
}

func (d pageData) FilterActive(kind, value string) bool {
	return d.Filter.has(kind, value)
}

func (d pageData) removeHref(kind, value string) string {
	q := d.query()
	if isViewChip(kind) {
		q.Del(kind)
		return d.href(q)
	}
	setList(q, kind, removeFold(d.Filter.list(kind), value))
	return d.href(q)
}

// ClearHref drops every filter but keeps the viewer's refresh choice.
func (d pageData) ClearHref() string {
	q := d.query()
	for _, k := range allFilters {
		q.Del(k)
	}
	return d.href(q)
}

func (d pageData) Chips() []Chip {
	var out []Chip
	add := func(kind, value string) {
		out = append(out, Chip{Label: kind + " " + value, RemoveHref: d.removeHref(kind, value)})
	}
	for _, v := range d.Filter.Status {
		add(filterStatus, v)
	}
	for _, v := range d.Filter.Sync {
		add(filterSync, v)
	}
	if d.Filter.GPU != "" {
		add(filterGPU, d.Filter.GPU)
	}
	if d.Filter.Arch != "" {
		add(filterArch, d.Filter.Arch)
	}
	if d.Filter.Blocked != "" {
		add(filterBlocked, d.Filter.Blocked)
	}
	return out
}

// RefreshOptions builds the header picker. Each link carries the active filters over.
func (d pageData) RefreshOptions() []RefreshOption {
	out := make([]RefreshOption, 0, len(refreshChoices))
	for _, n := range refreshChoices {
		q := d.query()
		q.Set(paramRefresh, strconv.Itoa(n))
		label := "off"
		if n > 0 {
			label = strconv.Itoa(n) + "s"
		}
		out = append(out, RefreshOption{Label: label, Href: d.href(q), Active: d.RefreshSeconds == n})
	}
	return out
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	snap, err := s.provider.Get(r.Context())
	query := r.URL.Query()

	data := pageData{
		EnvName:        s.cfg.EnvName,
		EnvType:        s.cfg.EnvType,
		Region:         s.cfg.Region,
		ClusterName:    s.cfg.ClusterName,
		BasePath:       s.cfg.BasePath,
		ArgoCDUIBase:   strings.TrimRight(s.cfg.ArgoCDUIBase, "/"),
		RefreshSeconds: refreshSeconds(query, s.cfg.RefreshSeconds),
		BuildVersion:   s.cfg.BuildVersion,
		Snapshot:       snap,
		Filter:         ParseFilter(query),
		LocalMode:      s.cfg.LocalMode,
		Query:          query,
		HasData:        snap != nil,
	}
	if err != nil {
		data.Error = truncate(err.Error(), 200)
		data.ClusterError = classifyClusterError(err)
	}
	if snap != nil {
		if data.EnvType == "" {
			data.EnvType = snap.EnvType
		}
		data.MultiSource = len(snap.Sources) > 1
		// An ArgoCD deployment whose ROOT_APP_NAME matches nothing keeps showing the
		// line reading "unknown": that is the documented symptom of a misconfigured
		// ROOT_APP_NAME and hiding it would hide the diagnosis.
		data.ShowRootLine = snap.HasRoot || hasSource(snap.Sources, status.SourceArgoCD)
		data.Services = data.Filter.Apply(snap.Services)
		// Looking at the accelerator view is a question about what is using devices
		// now, so the ones that are answer it first. The global order is worst-first,
		// which would bury them under everything merely waiting or stopped.
		if data.Filter.GPU != "" {
			sortRunningFirst(data.Services)
		}
		data.Shown = len(data.Services)
		data.AgeSeconds = s.ageSeconds(snap)
		data.Stale = snap.Stale
	}

	var buf bytes.Buffer
	if execErr := s.tmpl.ExecuteTemplate(&buf, "index.html", data); execErr != nil {
		log.Printf("render page: %v", execErr)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, wErr := buf.WriteTo(w); wErr != nil {
		log.Printf("write page: %v", wErr)
	}
}

func hasSource(sources []status.Source, want status.Source) bool {
	for _, s := range sources {
		if s == want {
			return true
		}
	}
	return false
}

// stateGlyph pairs every state with a shape so the pill never relies on color
// alone, required for colorblind readers and for forced-colors mode.
func stateGlyph(state string) string {
	switch state {
	case "OK":
		return "\u2713"
	case "DEGRADED":
		return "\u2715"
	case "WARNING":
		return "!"
	case "PROGRESSING":
		return "\u21bb"
	case "DRIFT":
		return "\u2260"
	case "PRUNE":
		return "\u2298"
	case "SUSPENDED":
		return "\u23f8"
	default:
		return "\u2022"
	}
}

// clip shortens a value for a dense table cell; the full value stays in a title attribute.
func clip(n int, v string) string {
	if n <= 1 || len(v) <= n {
		return v
	}
	return v[:n-1] + "\u2026"
}

// icon returns a small inline SVG. Inline keeps the page a single self-contained
// file with no external requests, which is the whole point of this UI. Icons are
// decorative: every one sits beside a text label, so nothing depends on reading them.
func icon(name string) template.HTML {
	const open = `<svg class="ic" viewBox="0 0 16 16" width="13" height="13" fill="none" ` +
		`stroke="currentColor" stroke-width="1.5" stroke-linecap="round" ` +
		`stroke-linejoin="round" aria-hidden="true" focusable="false">`
	var body string
	switch name {
	case "node": // stacked servers
		body = `<rect x="2" y="2.5" width="12" height="4.5" rx="1"/><rect x="2" y="9" width="12" height="4.5" rx="1"/><path d="M4.5 4.75h.01M4.5 11.25h.01"/>`
	case "cpu": // chip with pins
		body = `<rect x="4.5" y="4.5" width="7" height="7" rx="1"/><path d="M6.5 2v2.5M9.5 2v2.5M6.5 11.5V14M9.5 11.5V14M2 6.5h2.5M2 9.5h2.5M11.5 6.5H14M11.5 9.5H14"/>`
	case "gpu": // a graphics card: board, fan, and the pins it seats on
		body = `<rect x="1.5" y="3.5" width="13" height="8" rx="1.5"/><circle cx="5.8" cy="7.5" r="2.1"/><path d="M10 5.8v3.4M12.2 5.8v3.4"/><path d="M4.5 11.5v2M11 11.5v2"/>`
	case "card": // stacked cards, for a count of physical GPUs
		body = `<rect x="2" y="5.5" width="9" height="6" rx="1"/><path d="M5 3.5h9a1 1 0 0 1 1 1v6"/>`
	case "arch": // two blocks, for the architecture split
		body = `<rect x="2" y="3" width="5" height="10" rx="1"/><rect x="9" y="3" width="5" height="10" rx="1"/>`
	case "warn": // triangle with a bang
		body = `<path d="M8 2.2 1.6 13.2h12.8L8 2.2Z"/><path d="M8 6.4v3.1M8 11.4h.01"/>`
	case "unmanaged": // broken link: running, but outside gitops
		body = `<path d="M6.5 9.5L4.8 11.2a2.4 2.4 0 0 1-3.4-3.4L3.1 6.1"/><path d="M9.5 6.5l1.7-1.7a2.4 2.4 0 0 1 3.4 3.4l-1.7 1.7"/><path d="M6 10L10 6"/>`
	case "mesh": // shield outline, for the mTLS gauge
		body = `<path d="M8 2 2.5 4v3.8c0 3.6 2.3 6.2 5.5 6.7 3.2-.5 5.5-3.1 5.5-6.7V4L8 2Z"/>`
	default:
		return ""
	}
	return template.HTML(open + body + `</svg>`)
}

// AnyFilter reports whether the page is narrowed at all, by row filters or by view.
func (d pageData) AnyFilter() bool { return d.Filter.Active() || d.Filter.View != "" }

// brand is the logo mark, inlined so the page stays one self-contained file.
func brand() template.HTML {
	return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="19" height="19" class="brand" role="img" aria-label="k8s-status"><title>k8s-status</title> <rect x="6" y="12" width="52" height="12" rx="6" fill="#e6f4ea"/> <rect x="6" y="28" width="46" height="12" rx="6" fill="#feefc3"/> <rect x="6" y="44" width="40" height="12" rx="6" fill="#fce8e6"/> <circle cx="16" cy="18" r="4" fill="#137333"/> <circle cx="16" cy="34" r="4" fill="#a15c00"/> <circle cx="16" cy="50" r="4" fill="#c5221f"/> </svg>`)
}

// sortRunningFirst orders accelerator rows by what they are doing: holding devices,
// then waiting for one, then stopped. Ties keep the worst-first order underneath.
func sortRunningFirst(services []status.Service) {
	rank := func(s status.Service) int {
		switch {
		case !s.GPU:
			return 3
		case s.GPUAlloc.Waiting():
			return 1
		case s.GPUAlloc.ScaledToZero():
			return 2
		default:
			return 0
		}
	}
	sort.SliceStable(services, func(i, j int) bool {
		return rank(services[i]) < rank(services[j])
	})
}

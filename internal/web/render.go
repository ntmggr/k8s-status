package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ntmggr/srv-status/internal/status"
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

	Snapshot   *status.Snapshot
	Services   []status.Service
	Filter     Filter
	Query      url.Values
	Shown      int
	AgeSeconds int
	Stale      bool
	Error      string
	HasData    bool
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

// FilterHref toggles one selection: clicking an active tile clears it, clicking an
// inactive one adds it. Everything else in the query, refresh included, is preserved.
func (d pageData) FilterHref(kind, value string) string {
	q := d.query()
	if kind == filterGPU {
		if strings.EqualFold(q.Get(filterGPU), value) {
			q.Del(filterGPU)
		} else {
			q.Set(filterGPU, value)
		}
		return d.href(q)
	}
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
	if kind == filterGPU {
		q.Del(filterGPU)
		return d.href(q)
	}
	setList(q, kind, removeFold(d.Filter.list(kind), value))
	return d.href(q)
}

// ClearHref drops every filter but keeps the viewer's refresh choice.
func (d pageData) ClearHref() string {
	q := d.query()
	q.Del(filterStatus)
	q.Del(filterSync)
	q.Del(filterGPU)
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
		Query:          query,
		HasData:        snap != nil,
	}
	if err != nil {
		data.Error = truncate(err.Error(), 200)
	}
	if snap != nil {
		if data.EnvType == "" {
			data.EnvType = snap.EnvType
		}
		data.Services = data.Filter.Apply(snap.Services)
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

// stateGlyph pairs every state with a shape so the pill never relies on color
// alone — required for colorblind readers and for forced-colors mode.
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

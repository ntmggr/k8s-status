package web

import (
	"encoding/json"
	"net/http"
	"time"
)

// versionsResponse is a deliberately narrow view: what is deployed, and at what
// version. /api/status carries the same facts plus health, counts and components,
// which is more than you want when the question is only "what version is where".
type versionsResponse struct {
	Env       string           `json:"env,omitempty"`
	Cluster   string           `json:"cluster,omitempty"`
	CheckedAt string           `json:"checkedAt,omitempty"`
	Count     int              `json:"count"`
	Filters   *filtersJSON     `json:"filters,omitempty"`
	Services  []serviceVersion `json:"services"`
	Error     *string          `json:"error"`
}

type serviceVersion struct {
	Name string `json:"name"`
	// AppVersion is the running container image tag: the version of the software.
	AppVersion string `json:"appVersion,omitempty"`
	// ChartVersion is the git ref the GitOps controller tracks for it.
	ChartVersion string `json:"chartVersion,omitempty"`
	Revision     string `json:"revision,omitempty"`
	Image        string `json:"image,omitempty"`
	Source       string `json:"source,omitempty"`
	State        string `json:"state,omitempty"`
}

// handleVersions answers "what is deployed here and at what version" in a form
// that is pleasant to diff between clusters or feed to a script. It honours the
// same filters as the page, so ?status=DEGRADED narrows it the same way.
func (s *Server) handleVersions(w http.ResponseWriter, r *http.Request) {
	snap, err := s.provider.Get(r.Context())
	filter := ParseFilter(r.URL.Query())

	out := versionsResponse{
		Env:      s.cfg.EnvName,
		Cluster:  s.cfg.ClusterName,
		Services: []serviceVersion{},
	}
	if err != nil {
		msg := truncate(err.Error(), 200)
		out.Error = &msg
	}
	if snap != nil {
		if !snap.CheckedAt.IsZero() {
			out.CheckedAt = snap.CheckedAt.UTC().Format(time.RFC3339)
		}
		for _, svc := range filter.Apply(snap.Services) {
			out.Services = append(out.Services, serviceVersion{
				Name:         svc.Name,
				AppVersion:   svc.AppVersion,
				ChartVersion: svc.Version,
				Revision:     svc.Revision,
				Image:        svc.Image,
				Source:       string(svc.Source),
				State:        string(svc.State),
			})
		}
	}
	out.Count = len(out.Services)
	if filter.Active() {
		out.Filters = &filtersJSON{
			Status:  filter.Status,
			Sync:    filter.Sync,
			GPU:     filter.GPU,
			Matched: out.Count,
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if encErr := json.NewEncoder(w).Encode(out); encErr != nil {
		return
	}
}

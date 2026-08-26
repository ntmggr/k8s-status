package web

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ntmggr/k8s-status/internal/status"
)

type Config struct {
	// LocalMode is set when the binary is talking to a proxied API from outside the
	// cluster. Worth showing: the permissions in play are the operator's kubeconfig,
	// not the ServiceAccount a real deployment would be limited to.
	LocalMode      bool
	EnvName        string
	EnvType        string
	Region         string
	ClusterName    string
	BasePath       string
	ArgoCDUIBase   string
	RefreshSeconds int
	BuildVersion   string
}

type Provider interface {
	Get(ctx context.Context) (*status.Snapshot, error)
}

type Server struct {
	cfg      Config
	provider Provider
	tmpl     *template.Template
	now      func() time.Time
}

func NewServer(cfg Config, p Provider) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	cfg.BasePath = NormalizeBasePath(cfg.BasePath)
	if cfg.RefreshSeconds <= 0 {
		cfg.RefreshSeconds = 30
	}
	return &Server{cfg: cfg, provider: p, tmpl: tmpl, now: time.Now}, nil
}

// NormalizeBasePath ensures a leading slash and no trailing slash; "" means root.
func NormalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	base := s.cfg.BasePath

	mux.HandleFunc("GET "+base+"/{$}", s.handlePage)
	mux.HandleFunc("GET "+base+"/api/status", s.handleAPI)
	mux.HandleFunc("GET "+base+"/api/versions", s.handleVersions)
	mux.HandleFunc("GET "+base+"/healthz", s.handleHealthz)

	if base != "" {
		mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, base+"/", http.StatusFound)
		})
	}
	return mux
}

// handleHealthz is the liveness probe and must never touch the Kubernetes API.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type summaryJSON struct {
	Total       int `json:"total"`
	OK          int `json:"ok"`
	Degraded    int `json:"degraded"`
	Warning     int `json:"warning"`
	Progressing int `json:"progressing"`
	Drift       int `json:"drift"`
	Prune       int `json:"prune"`
	Suspended   int `json:"suspended"`
	Hidden      int `json:"hidden"`
	GPU         int `json:"gpu"`
}

type serviceJSON struct {
	Name string `json:"name"`
	// Source is always set. Namespace and Kind are only carried by Flux rows.
	Source     string          `json:"source,omitempty"`
	Namespace  string          `json:"namespace,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Version    string          `json:"version"`
	Revision   string          `json:"revision"`
	RepoURL    string          `json:"repoUrl,omitempty"`
	AppVersion string          `json:"appVersion,omitempty"`
	GPU        bool            `json:"gpu,omitempty"`
	Image      string          `json:"image,omitempty"`
	State      string          `json:"state"`
	Sync       string          `json:"sync"`
	Health     string          `json:"health"`
	Detail     string          `json:"detail"`
	Components []componentJSON `json:"components,omitempty"`
}

type componentJSON struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Sync      string `json:"sync"`
}

// nodesJSON is omitted entirely when NODE_STATS is off.
type nodesJSON struct {
	Total       int            `json:"total"`
	CPUNodes    int            `json:"cpuNodes"`
	GPUNodes    int            `json:"gpuNodes"`
	GPUs        int            `json:"gpus"`
	GPUServices int            `json:"gpuServices"`
	Arch        map[string]int `json:"arch,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// unmanagedJSON is omitted entirely when UNMANAGED is off. These are workloads running
// outside GitOps, so they are deliberately kept out of summary and services.
type unmanagedJSON struct {
	Count   int            `json:"count"`
	Scanned int            `json:"scanned"`
	Ignored int            `json:"ignored,omitempty"`
	Items   []workloadJSON `json:"items"`
	Error   string         `json:"error,omitempty"`
}

type workloadJSON struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	ManagedBy string `json:"managedBy"`
	Ready     int    `json:"ready"`
	Desired   int    `json:"desired"`
	Version   string `json:"version,omitempty"`
	Image     string `json:"image,omitempty"`
	State     string `json:"state"`
}

// fluxJSON is omitted entirely unless flux is one of SOURCES. Flux rows live in
// services and summary like any other, so this only reports the read itself.
type fluxJSON struct {
	HelmReleases   int    `json:"helmReleases"`
	Kustomizations int    `json:"kustomizations"`
	Error          string `json:"error,omitempty"`
}

// filtersJSON echoes the applied selection so the payload is self-describing.
// Omitted when no filter is active.
type filtersJSON struct {
	Status  []string `json:"status,omitempty"`
	Sync    []string `json:"sync,omitempty"`
	GPU     string   `json:"gpu,omitempty"`
	Matched int      `json:"matched"`
}

type apiResponse struct {
	Schema         int            `json:"schema"`
	Env            string         `json:"env"`
	EnvType        string         `json:"envType"`
	Region         string         `json:"region"`
	ClusterName    string         `json:"clusterName"`
	ClusterPath    string         `json:"clusterPath"`
	Sources        []string       `json:"sources,omitempty"`
	Version        string         `json:"version"`
	Revision       string         `json:"revision"`
	RootHealth     string         `json:"rootHealth"`
	RootSync       string         `json:"rootSync"`
	Phase          string         `json:"phase"`
	Message        string         `json:"message"`
	LastDeployedAt string         `json:"lastDeployedAt"`
	LastDeployID   int            `json:"lastDeployId"`
	Summary        summaryJSON    `json:"summary"`
	Nodes          *nodesJSON     `json:"nodes,omitempty"`
	Unmanaged      *unmanagedJSON `json:"unmanaged,omitempty"`
	Flux           *fluxJSON      `json:"flux,omitempty"`
	Filters        *filtersJSON   `json:"filters,omitempty"`
	Services       []serviceJSON  `json:"services"`
	CheckedAt      string         `json:"checkedAt"`
	AgeSeconds     int            `json:"ageSeconds"`
	Stale          bool           `json:"stale"`
	Error          *string        `json:"error"`
}

// handleAPI always answers 200 so scrapers can distinguish "app up, cluster read failed"
// from "app down".
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	snap, err := s.provider.Get(r.Context())
	filter := ParseFilter(r.URL.Query())

	resp := apiResponse{
		Schema:      1,
		Env:         s.cfg.EnvName,
		EnvType:     s.cfg.EnvType,
		Region:      s.cfg.Region,
		ClusterName: s.cfg.ClusterName,
		ClusterPath: s.cfg.ClusterName,
		Services:    []serviceJSON{},
	}
	if err != nil {
		msg := truncate(err.Error(), 200)
		resp.Error = &msg
	}

	if snap != nil {
		if resp.EnvType == "" {
			resp.EnvType = snap.EnvType
		}
		resp.Version = snap.Version
		resp.Revision = snap.Revision
		resp.RootHealth = snap.RootHealth
		resp.RootSync = snap.RootSync
		resp.Phase = snap.Phase
		resp.Message = snap.Message
		resp.LastDeployedAt = snap.LastDeployedAt
		resp.LastDeployID = snap.LastDeployID
		resp.Summary = summaryJSON{
			Total:       snap.Summary.Total,
			OK:          snap.Summary.OK,
			Degraded:    snap.Summary.Degraded,
			Warning:     snap.Summary.Warning,
			Progressing: snap.Summary.Progressing,
			Drift:       snap.Summary.Drift,
			Prune:       snap.Summary.Prune,
			Suspended:   snap.Summary.Suspended,
			Hidden:      snap.Summary.Hidden,
			GPU:         snap.Summary.GPU,
		}
		resp.Sources = sources(snap)
		resp.Nodes = nodes(snap)
		resp.Unmanaged = unmanaged(snap)
		resp.Flux = flux(snap)
		// summary stays whole-cluster: filtering selects rows, it does not change counts.
		services := filter.Apply(snap.Services)
		if filter.Active() {
			resp.Filters = &filtersJSON{
				Status:  filter.Status,
				Sync:    filter.Sync,
				GPU:     filter.GPU,
				Matched: len(services),
			}
		}
		for _, svc := range services {
			resp.Services = append(resp.Services, serviceJSON{
				Name:       svc.Name,
				Source:     string(svc.Source),
				Namespace:  svc.Namespace,
				Kind:       svc.Kind,
				Version:    svc.Version,
				Revision:   svc.Revision,
				RepoURL:    svc.RepoURL,
				AppVersion: svc.AppVersion,
				GPU:        svc.GPU,
				Image:      svc.Image,
				State:      string(svc.State),
				Sync:       svc.Sync,
				Health:     svc.Health,
				Detail:     svc.Detail,
				Components: components(svc),
			})
		}
		if !snap.CheckedAt.IsZero() {
			resp.CheckedAt = snap.CheckedAt.UTC().Format(time.RFC3339)
			resp.AgeSeconds = s.ageSeconds(snap)
		}
		resp.Stale = snap.Stale
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if encErr := json.NewEncoder(w).Encode(resp); encErr != nil {
		log.Printf("encode api response: %v", encErr)
	}
}

func (s *Server) ageSeconds(snap *status.Snapshot) int {
	if snap == nil || snap.CheckedAt.IsZero() {
		return 0
	}
	age := int(s.now().Sub(snap.CheckedAt).Seconds())
	if age < 0 {
		return 0
	}
	return age
}

func sources(snap *status.Snapshot) []string {
	if len(snap.Sources) == 0 {
		return nil
	}
	out := make([]string, 0, len(snap.Sources))
	for _, s := range snap.Sources {
		out = append(out, string(s))
	}
	return out
}

func flux(snap *status.Snapshot) *fluxJSON {
	if snap.Flux == nil {
		return nil
	}
	return &fluxJSON{
		HelmReleases:   snap.Flux.HelmReleases,
		Kustomizations: snap.Flux.Kustomizations,
		Error:          snap.Flux.Error,
	}
}

func nodes(snap *status.Snapshot) *nodesJSON {
	if snap.Nodes == nil {
		return nil
	}
	n := snap.Nodes
	out := &nodesJSON{
		Total:       n.Total,
		CPUNodes:    n.CPUNodes,
		GPUNodes:    n.GPUNodes,
		GPUs:        n.GPUs,
		GPUServices: snap.Summary.GPU,
		Error:       n.Error,
	}
	if len(n.Arch) > 0 {
		out.Arch = make(map[string]int, len(n.Arch))
		for _, a := range n.Arch {
			out.Arch[a.Arch] = a.Count
		}
	}
	return out
}

func unmanaged(snap *status.Snapshot) *unmanagedJSON {
	if snap.Unmanaged == nil {
		return nil
	}
	u := snap.Unmanaged
	out := &unmanagedJSON{
		Count:   u.Count,
		Scanned: u.Scanned,
		Ignored: u.Ignored,
		Items:   make([]workloadJSON, 0, len(u.Items)),
		Error:   u.Error,
	}
	for _, w := range u.Items {
		out.Items = append(out.Items, workloadJSON{
			Namespace: w.Namespace,
			Kind:      w.Kind,
			Name:      w.Name,
			ManagedBy: w.ManagedBy,
			Ready:     w.Ready,
			Desired:   w.Desired,
			Version:   w.Version,
			Image:     w.Image,
			State:     string(w.State),
		})
	}
	return out
}

func components(svc status.Service) []componentJSON {
	if len(svc.Components) == 0 {
		return nil
	}
	out := make([]componentJSON, 0, len(svc.Components))
	for _, c := range svc.Components {
		out = append(out, componentJSON{Kind: c.Kind, Name: c.Name, Namespace: c.Namespace, Sync: c.Sync})
	}
	return out
}

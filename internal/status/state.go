package status

import (
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ntmggr/srv-status/internal/argocd"
)

type State string

const (
	StateOK          State = "OK"
	StateDegraded    State = "DEGRADED"
	StateWarning     State = "WARNING"
	StateProgressing State = "PROGRESSING"
	StateDrift       State = "DRIFT"
	StatePrune       State = "PRUNE"
	StateSuspended   State = "SUSPENDED"
)

const (
	healthUnknown = "Unknown"
	syncOutOfSync = "OutOfSync"
	maxTextLen    = 200
	maxComponents = 25
	envTypeLabel  = "EnvType"
)

var severity = map[State]int{
	StateDegraded:    0,
	StateWarning:     1,
	StateProgressing: 2,
	StateDrift:       3,
	StatePrune:       4,
	StateSuspended:   5,
	StateOK:          6,
}

// Options controls how a raw Application list is folded into a Snapshot.
type Options struct {
	RootAppName string
	IgnoreGlobs []string
	// GPUGlobs mark services as GPU-backed. Name-based because it needs no extra
	// cluster permissions and stays portable across clusters.
	GPUGlobs []string
	// SidecarImages are image names that never represent the service itself.
	// Empty means use the built-in list.
	SidecarImages []string
}

// Component is an individual Kubernetes resource inside a service that is not
// matching git. ArgoCD does not populate per-resource health in the Application
// CR, so only sync state is available here.
type Component struct {
	Kind      string
	Name      string
	Namespace string
	Sync      string
}

type Service struct {
	Name       string
	Version    string
	Revision   string
	RepoURL    string
	AppVersion string
	Image      string
	GPU        bool
	State      State
	Sync       string
	Health     string
	Detail     string
	Components []Component
}

type Summary struct {
	Total       int
	OK          int
	Degraded    int
	Warning     int
	Progressing int
	Drift       int
	Prune       int
	Suspended   int
	Hidden      int
	GPU         int
}

type Snapshot struct {
	EnvType        string
	Version        string
	Revision       string
	RepoURL        string
	RootHealth     string
	RootSync       string
	Phase          string
	Message        string
	LastDeployedAt string
	LastDeployID   int
	Summary        Summary
	Services       []Service
	// Nodes is nil unless NODE_STATS is enabled.
	Nodes     *NodeStats
	CheckedAt time.Time
	Stale     bool
}

// Build folds the Application list into the rendered view of the environment.
func Build(list *argocd.ApplicationList, opts Options) Snapshot {
	sidecars := opts.SidecarImages
	if len(sidecars) == 0 {
		sidecars = defaultSidecarImages
	}
	snap := Snapshot{Services: []Service{}}
	if list == nil {
		return snap
	}

	var root *argocd.Application
	children := make([]argocd.Application, 0, len(list.Items))
	for i := range list.Items {
		app := list.Items[i]
		if app.Metadata.Name == opts.RootAppName {
			root = &list.Items[i]
			continue
		}
		children = append(children, app)
	}

	prune := map[string]bool{}
	rootSettled := true
	if root != nil {
		for _, res := range root.Status.Resources {
			if res.RequiresPruning {
				prune[res.Name] = true
			}
		}
		rootSettled = root.Status.OperationState.Phase != "Running"

		snap.EnvType = root.Metadata.Labels[envTypeLabel]
		snap.Version = root.Spec.Source.TargetRevision
		snap.Revision = root.Status.Sync.Revision
		snap.RootHealth = normalizeHealth(root.Status.Health.Status)
		snap.RootSync = root.Status.Sync.Status
		snap.RepoURL = root.Spec.Source.RepoURL
		snap.Phase = root.Status.OperationState.Phase
		snap.Message = truncate(root.Status.OperationState.Message, maxTextLen)
		if h, ok := latestHistory(root.Status.History); ok {
			snap.LastDeployedAt = h.DeployedAt
			snap.LastDeployID = h.ID
		}
	}

	for _, app := range children {
		name := app.Metadata.Name
		if matchesAny(name, opts.IgnoreGlobs) {
			snap.Summary.Hidden++
			continue
		}

		health := normalizeHealth(app.Status.Health.Status)
		sync := app.Status.Sync.Status
		st := classify(name, health, sync, prune, rootSettled)

		img := componentImage(app.Metadata.Name, app.Status.Summary.Images, sidecars)
		snap.Services = append(snap.Services, Service{
			Name:       name,
			Version:    app.Spec.Source.TargetRevision,
			RepoURL:    app.Spec.Source.RepoURL,
			GPU:        matchesAny(app.Metadata.Name, opts.GPUGlobs),
			AppVersion: img.Tag,
			Image:      img.Full,
			Revision:   app.Status.Sync.Revision,
			State:      st,
			Sync:       sync,
			Health:     health,
			Detail:     truncate(detailFor(app, st), maxTextLen),
			Components: componentsOf(app, st),
		})

		snap.Summary.Total++
		if matchesAny(name, opts.GPUGlobs) {
			snap.Summary.GPU++
		}
		switch st {
		case StateOK:
			snap.Summary.OK++
		case StateDegraded:
			snap.Summary.Degraded++
		case StateWarning:
			snap.Summary.Warning++
		case StateProgressing:
			snap.Summary.Progressing++
		case StateDrift:
			snap.Summary.Drift++
		case StatePrune:
			snap.Summary.Prune++
		case StateSuspended:
			snap.Summary.Suspended++
		}
	}

	sort.Slice(snap.Services, func(i, j int) bool {
		a, b := snap.Services[i], snap.Services[j]
		if severity[a.State] != severity[b.State] {
			return severity[a.State] < severity[b.State]
		}
		return a.Name < b.Name
	})

	return snap
}

func classify(name, health, sync string, prune map[string]bool, rootSettled bool) State {
	switch {
	case prune[name]:
		return StatePrune
	case health == "Degraded" || health == "Missing":
		return StateDegraded
	case health == healthUnknown:
		// ArgoCD could not determine health: worth attention, but not proof of failure.
		return StateWarning
	case health == "Progressing" || (!rootSettled && sync == syncOutOfSync):
		return StateProgressing
	case health == "Suspended":
		return StateSuspended
	case sync == syncOutOfSync && rootSettled:
		// Healthy but not matching git: config drift, not an outage.
		return StateDrift
	default:
		return StateOK
	}
}

func detailFor(app argocd.Application, st State) string {
	if msg := strings.TrimSpace(app.Status.Health.Message); msg != "" {
		return msg
	}
	// Drift means ArgoCD reports the workload Healthy — its pods pass their
	// readiness and liveness checks — while the manifests differ from git.
	// Say so explicitly, otherwise an OutOfSync row reads as an outage.
	if st == StateDrift {
		return "pods healthy and passing health checks; config differs from git"
	}
	// A successful sync message carries no signal, so only surface failing operations.
	if app.Status.OperationState.Phase != "Succeeded" {
		return strings.TrimSpace(app.Status.OperationState.Message)
	}
	return ""
}

func normalizeHealth(h string) string {
	if strings.TrimSpace(h) == "" {
		return healthUnknown
	}
	return h
}

func latestHistory(entries []argocd.HistoryEntry) (argocd.HistoryEntry, bool) {
	var best argocd.HistoryEntry
	found := false
	for _, e := range entries {
		if !found || e.ID > best.ID {
			best, found = e, true
		}
	}
	return best, found
}

func matchesAny(name string, globs []string) bool {
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if ok, err := path.Match(g, name); err == nil && ok {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// componentsOf lists the resources of a service that are not matching git.
// Healthy services are skipped: their component list is noise.
func componentsOf(app argocd.Application, st State) []Component {
	if st == StateOK {
		return nil
	}
	out := make([]Component, 0, 4)
	for _, r := range app.Status.Resources {
		if r.Status != syncOutOfSync {
			continue
		}
		out = append(out, Component{Kind: r.Kind, Name: r.Name, Namespace: r.Namespace, Sync: r.Status})
		if len(out) == maxComponents {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

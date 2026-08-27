package status

import (
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ntmggr/k8s-status/internal/argocd"
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
	// Sources are the enabled GitOps controllers. A source that is absent here is
	// never queried.
	Sources     []Source
	RootAppName string
	IgnoreGlobs []string
	// GPUGlobs mark services as GPU-backed. Name-based because it needs no extra
	// cluster permissions and stays portable across clusters.
	GPUGlobs []string
	// AcceleratorResources overrides which resources count as accelerators. Empty
	// discovers them from what the nodes advertise, which is right on every cluster
	// seen so far and needs no per-vendor knowledge.
	AcceleratorResources []string
	// SidecarImages are image names that never represent the service itself.
	// Empty means use the built-in list.
	SidecarImages []string
	// UnmanagedIgnoreNS are namespace globs excluded from the unmanaged workload
	// section. Same path.Match semantics as IgnoreGlobs.
	UnmanagedIgnoreNS []string
}

// Component is an individual Kubernetes resource inside a service that is not
// matching git. ArgoCD does not populate per-resource health in the Application
// CR, so only sync state is available here.
// GPUAllocation separates three states that look identical in a boolean GPU marker:
// a service holding devices, one deliberately scaled to zero, and one that wants
// devices and cannot get them. The last is the interesting one and the only one that
// needs attention.
type GPUAllocation struct {
	// PerReplica is what one replica requests. Zero means the service never names the
	// resource and reaches its device through the runtime, so how much it uses is not
	// knowable from the API.
	PerReplica int
	Desired    int
	Ready      int
	// Allocated is PerReplica * Ready: devices the scheduler has actually handed over.
	Allocated int
}

// Waiting reports a service that asked for devices and did not get them. Distinct from
// scaled to zero, which is deliberate, and from holding fewer than it wants only
// because it is mid-rollout, which still shows here and is worth seeing.
func (g GPUAllocation) Waiting() bool { return g.Desired > 0 && g.Ready < g.Desired }

// ScaledToZero reports a service asking for no replicas. Deliberately not called
// "idle": that word describes a device that is allocated and doing no work, which is a
// utilisation fact this project cannot see. What is known here is only that nothing is
// running, and whether that is a deliberate stop or an autoscaler at rest is not
// visible from the workload alone.
func (g GPUAllocation) ScaledToZero() bool { return g.Desired == 0 }

// Unmeasured reports a service that reaches a device without requesting it, so the
// API cannot say how much it holds.
func (g GPUAllocation) Unmeasured() bool { return g.PerReplica == 0 }

type Component struct {
	Kind      string
	Name      string
	Namespace string
	Sync      string
}

type Service struct {
	Name string
	// Namespace and Kind are only set for Flux rows: HelmReleases and Kustomizations
	// are spread across namespaces and two kinds, while ArgoCD Applications are all
	// Applications in one namespace and need no disambiguation.
	Namespace  string
	Kind       string
	Source     Source
	Version    string
	Revision   string
	RepoURL    string
	AppVersion string
	Image      string
	GPU        bool
	// Blocked is set when the scheduler could not place this service's pods. Nil for
	// everything else, which is almost every row.
	Blocked *Blocked
	// Arch is set only when every node this service may run on shares one CPU
	// architecture. Empty means it can go either way, which is the common case.
	Arch string
	// GPUAlloc describes what this service asks of the accelerators and what it
	// actually holds. Only meaningful when GPU is set.
	GPUAlloc   GPUAllocation
	State      State
	Sync       string
	Health     string
	Detail     string
	Components []Component
	// Owned lists the workloads this service manages, used to recover an app version
	// when ArgoCD did not report one. Not rendered.
	Owned []Component
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
	// GPURunning holds devices right now. GPUWaiting asked and has not got one.
	// GPUStopped is asking for no replicas at all. Split out because a single GPU
	// count hides all three, and the one worth leading with is what is running.
	GPURunning int
	// GPUUnmeasured reach a device through the runtime without requesting one, so they
	// contribute nothing to the allocated total. Counted so the page can say that the
	// total is a floor rather than letting it read as the whole story.
	GPUUnmeasured int
	GPUWaiting    int
	GPUStopped    int
	// Blocked counts services the scheduler could not place, split by what ran out:
	// an accelerator, ordinary cpu or memory, or no matching node at all.
	Blocked          int
	BlockedGPU       int
	BlockedCPU       int
	BlockedPlacement int
}

type Snapshot struct {
	// ArchCounts tallies services pinned to a single CPU architecture. Kept here
	// rather than on Summary so Summary stays a comparable value.
	ArchCounts []ArchCount
	// Sources are the GitOps controllers this snapshot was read from, in the order
	// SOURCES named them. The Source column is only rendered when there is more than
	// one, so a single-source cluster keeps the uncluttered table.
	Sources []Source
	// HasRoot reports whether an ArgoCD root Application was found. Everything in the
	// environment header below comes from it, so a Flux-only cluster has none.
	HasRoot        bool
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
	Nodes *NodeStats
	// Unmanaged is nil unless UNMANAGED is enabled.
	Unmanaged *Unmanaged
	// Flux is nil unless flux is one of SOURCES.
	Flux      *FluxSection
	CheckedAt time.Time
	Stale     bool
}

// Build folds the Application list into the rendered view of the environment.
func Build(list *argocd.ApplicationList, opts Options) Snapshot {
	sidecars := opts.SidecarImages
	if len(sidecars) == 0 {
		sidecars = defaultSidecarImages
	}
	snap := Snapshot{Sources: opts.Sources, Services: []Service{}}
	if list == nil {
		return snap
	}

	// The app-of-apps is identified by shape, not by name: it is the Application
	// that owns other Applications. Hard-coding a name only ever works on the one
	// cluster it was written for. An explicit RootAppName still wins.
	rootName := opts.RootAppName
	if rootName == "" {
		rootName = detectRootApp(list.Items)
	}

	var root *argocd.Application
	children := make([]argocd.Application, 0, len(list.Items))
	for i := range list.Items {
		app := list.Items[i]
		if app.Metadata.Name == rootName {
			root = &list.Items[i]
			continue
		}
		children = append(children, app)
	}

	prune := map[string]bool{}
	rootSettled := true
	if root != nil {
		snap.HasRoot = true
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
		snap.addService(Service{
			Name:       name,
			Source:     SourceArgoCD,
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
			Owned:      ownedWorkloads(app),
		})
	}

	sortServices(snap.Services)
	return snap
}

// addService appends one row and folds it into the summary. Every source goes through
// here so the tiles keep summing to the service total whatever produced the row.
func (s *Snapshot) addService(svc Service) {
	s.Services = append(s.Services, svc)
	s.Summary.Total++
	if svc.GPU {
		s.Summary.GPU++
	}
	switch svc.State {
	case StateOK:
		s.Summary.OK++
	case StateDegraded:
		s.Summary.Degraded++
	case StateWarning:
		s.Summary.Warning++
	case StateProgressing:
		s.Summary.Progressing++
	case StateDrift:
		s.Summary.Drift++
	case StatePrune:
		s.Summary.Prune++
	case StateSuspended:
		s.Summary.Suspended++
	}
}

// rank is a row's place in the worst-first order. It is the state's severity, except
// that a service the scheduler cannot place never sorts below a warning: its pods are
// not running and no state field says so. ArgoCD called one such service Healthy while
// six of its pods could not be scheduled, and it sat at row 57 of 146.
func rank(s Service) int {
	r := severity[s.State]
	if s.Blocked != nil && r > severity[StateWarning] {
		return severity[StateWarning]
	}
	return r
}

// sortServices orders rows worst-first, then alphabetically. Source and namespace are
// the last tiebreakers: two sources can each own a row of the same name, and the order
// has to stay stable between refreshes.
func sortServices(services []Service) {
	sort.Slice(services, func(i, j int) bool {
		a, b := services[i], services[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Namespace < b.Namespace
	})
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

// detectRootApp returns the name of the Application that owns the most other
// Applications, or "" when nothing does. A flat ArgoCD install or a Flux-only
// cluster has no root, and the page renders without the environment header.
func detectRootApp(items []argocd.Application) string {
	best, bestN := "", 0
	for _, a := range items {
		n := 0
		for _, r := range a.Status.Resources {
			if r.Kind == "Application" && r.Group == "argoproj.io" {
				n++
			}
		}
		if n > bestN {
			best, bestN = a.Metadata.Name, n
		}
	}
	return best
}

// ownedWorkloads lists the Deployments, StatefulSets and DaemonSets an Application
// owns, from its own status.resources. Used to look up a running image when ArgoCD
// did not populate status.summary.images.
func ownedWorkloads(app argocd.Application) []Component {
	var out []Component
	for _, r := range app.Status.Resources {
		switch r.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			out = append(out, Component{Kind: r.Kind, Name: r.Name, Namespace: r.Namespace})
		}
	}
	return out
}

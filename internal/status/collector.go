package status

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ntmggr/k8s-status/internal/argocd"
	"github.com/ntmggr/k8s-status/internal/kube"
)

type Lister interface {
	ListApplications(ctx context.Context) (*argocd.ApplicationList, error)
}

// NodeLister is optional: it is only wired up when NODE_STATS is enabled, so the
// default deployment never touches the cluster-scoped nodes API.
type NodeLister interface {
	ListNodes(ctx context.Context) (*kube.NodeList, error)
}

// PendingLister reads pods the scheduler has not placed. Optional, like the others:
// without it the page simply does not say why anything is pending.
type PendingLister interface {
	ListPendingPods(ctx context.Context) (*kube.PodList, error)
}

// WorkloadLister is optional in the same way, for UNMANAGED. Listing workloads in
// every namespace needs a ClusterRole, so the default deployment never calls it.
type WorkloadLister interface {
	ListWorkloads(ctx context.Context) (*kube.WorkloadList, error)
}

// JobLister is optional in the same way, for JOBS. Job and CronJob are cluster-wide
// reads like Deployment/StatefulSet/DaemonSet, so this needs its own ClusterRole too.
type JobLister interface {
	ListJobs(ctx context.Context) (*kube.JobList, *kube.CronJobList, error)
}

// FluxLister is optional in the same way, for SOURCES. It is only wired up when flux is
// one of the enabled sources, so an ArgoCD-only deployment never calls the Flux APIs.
type FluxLister interface {
	ListFlux(ctx context.Context) (*kube.FluxList, error)
}

// MeshLister is optional, for the mesh mTLS gauge. It is only wired up when MESH_MTLS
// is enabled, so the default deployment never touches Istio's CRDs.
//
// ListPeerAuthentications is a cluster-wide list, a materially larger permission than
// MeshPolicy's single-object GET: it lets the per-service policy filter see namespace-
// and workload-scoped overrides, which can live in any namespace, not only the one
// MeshPolicy reads.
type MeshLister interface {
	DetectIstio(ctx context.Context) (string, error)
	MeshPolicy(ctx context.Context, groupVersion, namespace string) (*kube.PeerAuthentication, error)
	ListPeerAuthentications(ctx context.Context, groupVersion string) (*kube.PeerAuthenticationList, error)
}

// RunningLister reads running pods for the AZ-spread section. Optional, like the
// others: only wired up when AZ_SPREAD is enabled, and only meaningful alongside a
// node lister since zone labels come from the node list. Per-service mesh sidecar
// coverage rides along on this same read (see FillZones): it needs no lister of its
// own.
type RunningLister interface {
	ListRunningPods(ctx context.Context) (*kube.PodList, error)
}

// Collector caches one Snapshot for a TTL. The mutex is deliberately held across
// the upstream fetch so a burst of concurrent callers collapses into one request.
type Collector struct {
	lister Lister
	opts   Options
	ttl    time.Duration
	now    func() time.Time

	mu        sync.Mutex
	snap      *Snapshot
	nodes     NodeLister
	pending   PendingLister
	workloads WorkloadLister
	jobs      JobLister
	flux      FluxLister
	mesh      MeshLister
	// meshNamespace is where the mesh-wide PeerAuthentication lives; set alongside mesh
	// by WithMesh.
	meshNamespace string

	nodeStats *NodeStats
	nodesAt   time.Time
	nodeList  *kube.NodeList

	pendingPods *kube.PodList
	pendingAt   time.Time

	unmanaged    *Unmanaged
	unmanagedAt  time.Time
	workloadList *kube.WorkloadList

	jobsAt      time.Time
	jobList     *kube.JobList
	cronJobList *kube.CronJobList
	// fluxList is the most recent Flux read, kept so fetchWorkloads can tell a
	// Flux-managed Helm release apart from an unmanaged one. Set alongside snap.Flux,
	// so it is nil exactly when Flux is disabled or has not been read yet.
	fluxList *kube.FluxList

	meshSection *MeshSection
	meshAt      time.Time
	// peerAuths is every PeerAuthentication in the cluster, read alongside the
	// mesh-wide policy so FillZones can resolve each service's own effective mode.
	// Left nil when that list read is denied or fails: per-service policy then just
	// never gets filled, same "an optional extra degrades silently" contract as the
	// rest of this file's optional reads.
	peerAuths *kube.PeerAuthenticationList

	running     RunningLister
	runningAt   time.Time
	runningPods *kube.PodList
	zoneRead    *ZoneError
}

func NewCollector(lister Lister, opts Options, ttl time.Duration) *Collector {
	return &Collector{lister: lister, opts: opts, ttl: ttl, now: time.Now}
}

// WithNodes enables the cluster capacity section. Not called means the nodes API is
// never queried and Snapshot.Nodes stays nil.
func (c *Collector) WithNodes(nl NodeLister) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = nl
	return c
}

// WithUnmanaged enables the unmanaged workload section. Not called means the workloads
// API is never queried and Snapshot.Unmanaged stays nil.
func (c *Collector) WithUnmanaged(wl WorkloadLister) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workloads = wl
	return c
}

// WithJobs enables Job/CronJob reporting. Not called means the batch APIs are never
// queried and every service's Jobs list stays empty.
func (c *Collector) WithJobs(jl JobLister) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jobs = jl
	return c
}

// WithPending enables the "why is this pending" note. Not called means the pods API is
// never queried and no row is ever marked blocked.
func (c *Collector) WithPending(pl PendingLister) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = pl
	return c
}

// WithFlux enables the Flux source. Not called means the Flux APIs are never queried
// and Snapshot.Flux stays nil.
func (c *Collector) WithFlux(fl FluxLister) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flux = fl
	return c
}

// WithMesh enables the mesh mTLS gauge. Not called means Istio's APIs are never
// queried and Snapshot.Mesh stays nil. namespace is where the mesh-wide
// PeerAuthentication lives, normally "istio-system".
func (c *Collector) WithMesh(ml MeshLister, namespace string) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mesh = ml
	c.meshNamespace = namespace
	return c
}

// WithZones enables the per-service AZ-spread section, and along with it per-service
// mesh sidecar coverage (see FillZones). Not called means the running pods API is
// never queried and no Service ever gets a Zones, Nodes or Mesh answer. Only
// meaningful alongside WithNodes: zone labels come from the node list, which is the
// caller's responsibility to have wired up too.
func (c *Collector) WithZones(rl RunningLister) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = rl
	return c
}

// Get returns the cached snapshot, refreshing it when the TTL has expired.
// On a failed refresh it returns the last good snapshot marked stale, plus the error.
func (c *Collector) Get(ctx context.Context) (*Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.snap != nil && now.Sub(c.snap.CheckedAt) < c.ttl {
		return c.decorate(ctx, *c.snap), nil
	}

	// lister is nil when argocd is not one of SOURCES; Build treats a nil list as an
	// empty environment, so a Flux-only deployment never touches the ArgoCD API.
	var list *argocd.ApplicationList
	if c.lister != nil {
		// Detached for the same reason as the optional reads: this refresh serves
		// every viewer, so one of them navigating away must not abort it.
		lctx, lcancel := context.WithTimeout(context.WithoutCancel(ctx), sourceTimeout)
		defer lcancel()
		var err error
		if list, err = c.lister.ListApplications(lctx); err != nil {
			if c.snap != nil {
				stale := *c.snap
				stale.Stale = true
				return c.decorate(ctx, stale), err
			}
			return nil, err
		}
	}

	snap := Build(list, c.opts)
	// Flux rows belong in the service table and its counts, so they are read on the
	// same refresh rather than decorated on afterwards. A failed read is folded in as
	// a note: an optional source must not be able to blank the page.
	if c.flux != nil {
		fl, ferr := c.flux.ListFlux(ctx)
		snap.AppendFlux(fl, ferr, c.opts)
		// Keep the last good read on failure, same as the other optional sources: a
		// transient Flux error must not make every Flux-managed release flash into
		// the unmanaged list until the next successful read.
		if ferr == nil {
			c.fluxList = fl
		}
	}
	snap.CheckedAt = c.now()
	c.snap = &snap
	return c.decorate(ctx, snap), nil
}

// decorate returns a copy of the snapshot carrying the optional sections, so the
// cached snapshot is never mutated after callers have a pointer to it.
func (c *Collector) decorate(ctx context.Context, snap Snapshot) *Snapshot {
	c.refreshSources(ctx)
	c.attachNodes(ctx, &snap)
	c.attachUnmanaged(ctx, &snap)
	// After attachUnmanaged, same reason attachZones is: needs svc.Owned populated,
	// either from ArgoCD's own resource tree or FillOwnedFromLabels's fallback.
	c.attachJobs(ctx, &snap)
	c.attachPending(ctx, &snap)
	c.attachMesh(ctx, &snap)
	// After attachUnmanaged: FillOwnedFromLabels there populates svc.Owned, and
	// attachZones needs it for the same owner-matching machinery attachPending uses.
	// FillZones also tallies per-service mesh sidecar coverage in the same pass.
	c.attachZones(ctx, &snap)
	return &snap
}

// sourceTimeout bounds a refresh once it is detached from the request that started it.
const sourceTimeout = 25 * time.Second

// abandoned reports a read that was cut short rather than answered. Caching one would
// store "node stats unavailable" for the whole interval because a viewer navigated
// away, which is what it looked like: the page went blank until the cache expired.
func abandoned(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// refreshSources fetches the optional reads at the same time instead of one after
// another. They are independent, and run in series they added up: a cold request took
// nearly three seconds against a cluster where each read alone is well under one, and
// with a 15 second cache that landed on a real viewer several times a minute.
//
// Each goroutine owns its own fields, and the wait finishes before anything reads them,
// so the attach functions below still see a settled cache and stay unchanged.
func (c *Collector) refreshSources(ctx context.Context) {
	// Detach from the request. A viewer clicking to another view cancels their HTTP
	// request, and with a shared cache that cancellation would otherwise kill a
	// refresh every other viewer is waiting on, then be stored as though the cluster
	// had failed. The refresh keeps its own deadline instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceTimeout)
	defer cancel()

	var wg sync.WaitGroup
	stale := func(at time.Time, empty bool) bool { return empty || c.now().Sub(at) >= c.ttl }

	if c.nodes != nil && stale(c.nodesAt, c.nodeStats == nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fetchNodes(ctx)
		}()
	}
	if c.workloads != nil && stale(c.unmanagedAt, c.unmanaged == nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fetchWorkloads(ctx)
		}()
	}
	if c.jobs != nil && stale(c.jobsAt, c.jobList == nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fetchJobs(ctx)
		}()
	}
	if c.pending != nil && stale(c.pendingAt, c.pendingPods == nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fetchPending(ctx)
		}()
	}
	if c.mesh != nil && stale(c.meshAt, c.meshSection == nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fetchMesh(ctx)
		}()
	}
	if c.running != nil && stale(c.runningAt, c.runningPods == nil && c.zoneRead == nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fetchRunning(ctx)
		}()
	}
	wg.Wait()
}

func (c *Collector) fetchNodes(ctx context.Context) {
	list, err := c.nodes.ListNodes(ctx)
	if abandoned(err) {
		return // keep whatever was cached; a cancelled read says nothing about the cluster
	}
	var stats NodeStats
	if err != nil {
		stats = nodeStatsError(err)
	} else {
		stats = BuildNodeStats(list, DiscoverAccelerators(list, c.opts.AcceleratorResources))
	}
	c.nodeStats = &stats
	c.nodesAt = c.now()
	c.nodeList = list
}

func (c *Collector) fetchWorkloads(ctx context.Context) {
	list, err := c.workloads.ListWorkloads(ctx)
	if abandoned(err) {
		return
	}
	u := BuildUnmanaged(list, c.opts, fluxReleaseKeys(c.fluxList))
	if err != nil {
		u = unmanagedError(u, err)
	}
	c.unmanaged = &u
	c.unmanagedAt = c.now()
	c.workloadList = list
}

// fetchJobs keeps the last good read on failure, the same as fetchWorkloads: Jobs is
// informational only, so a transient error here must not blank out what was already
// known about a service's Jobs.
func (c *Collector) fetchJobs(ctx context.Context) {
	jobs, cronJobs, err := c.jobs.ListJobs(ctx)
	if err != nil {
		return
	}
	c.jobList = jobs
	c.cronJobList = cronJobs
	c.jobsAt = c.now()
}

func (c *Collector) fetchPending(ctx context.Context) {
	list, err := c.pending.ListPendingPods(ctx)
	if abandoned(err) {
		return
	}
	if err != nil {
		list = &kube.PodList{}
	}
	c.pendingPods = list
	c.pendingAt = c.now()
}

// fetchMesh detects Istio and, only when present, reads the mesh-wide
// PeerAuthentication. Detection runs on every refresh rather than once at startup: a
// cluster can gain or lose Istio without a restart.
func (c *Collector) fetchMesh(ctx context.Context) {
	gv, derr := c.mesh.DetectIstio(ctx)
	if abandoned(derr) {
		return // keep whatever was cached; a cancelled read says nothing about the cluster
	}
	// A discovery failure degrades to "not installed", the same fallback detectSources
	// already uses for its own probes: a transient API hiccup must not render the mesh
	// gauge as broken.
	installed := derr == nil && gv != ""

	var pa *kube.PeerAuthentication
	var perr error
	var peerAuths *kube.PeerAuthenticationList
	if installed {
		pa, perr = c.mesh.MeshPolicy(ctx, gv, c.meshNamespace)
		if abandoned(perr) {
			return
		}
		// Best-effort, independent of the mesh-wide read above: a denied or failed
		// list here must not take the mesh-wide gauge down with it, it only means no
		// per-service overrides get shown (FillZones leaves Policy unset).
		list, lerr := c.mesh.ListPeerAuthentications(ctx, gv)
		if abandoned(lerr) {
			return
		}
		if lerr == nil {
			peerAuths = list
		}
	}

	sec := BuildMesh(installed, gv, pa, perr)
	c.meshSection = &sec
	c.peerAuths = peerAuths
	c.meshAt = c.now()
}

// fetchRunning reads every running pod for the AZ-spread section. Unlike
// fetchPending, a failed read here is kept and surfaced as ZoneRead rather than
// swallowed: a denied or too-large read must produce a visible note, not a silently
// empty section.
func (c *Collector) fetchRunning(ctx context.Context) {
	list, err := c.running.ListRunningPods(ctx)
	if abandoned(err) {
		return // keep whatever was cached; a cancelled read says nothing about the cluster
	}
	if err != nil {
		c.runningPods = nil
		zr := zoneReadError(err)
		c.zoneRead = zr
		c.runningAt = c.now()
		return
	}
	c.runningPods = list
	c.zoneRead = nil
	c.runningAt = c.now()
}

func (c *Collector) attachNodes(_ context.Context, snap *Snapshot) {
	if c.nodes == nil {
		return
	}
	snap.Nodes = c.nodeStats
}

// attachPending explains pods the scheduler refused. A failed read is swallowed
// upstream: the note is an extra, and losing it must not take the page down.
func (c *Collector) attachPending(_ context.Context, snap *Snapshot) {
	if c.pending == nil {
		return
	}
	FillPending(snap, c.pendingPods, c.workloadList)
}

func (c *Collector) attachUnmanaged(_ context.Context, snap *Snapshot) {
	if c.workloads == nil {
		return
	}
	snap.Unmanaged = c.unmanaged
	// The same workload list recovers app versions ArgoCD did not report, says which
	// services actually ask for a device, and which are pinned to one architecture.
	// ArgoCD does not always say what an Application owns. Recover that first, or
	// everything below silently skips those services.
	FillOwnedFromLabels(snap, c.workloadList)
	FillMissingVersions(snap, c.workloadList)
	FillGPU(snap, c.workloadList, c.nodeList, DiscoverAccelerators(c.nodeList, c.opts.AcceleratorResources))
	FillArch(snap, c.workloadList, c.nodeList)
}

func (c *Collector) attachJobs(_ context.Context, snap *Snapshot) {
	if c.jobs == nil {
		return
	}
	FillJobs(snap, c.jobList, c.cronJobList)
}

func (c *Collector) attachMesh(_ context.Context, snap *Snapshot) {
	if c.mesh == nil {
		return
	}
	snap.Mesh = c.meshSection
}

// attachZones fills each service's AZ spread and mesh sidecar coverage. Must run after
// attachUnmanaged: FillOwnedFromLabels there populates svc.Owned, which the
// owner-matching this reuses from FillPending depends on, the same ordering FillGPU
// and FillArch already rely on.
func (c *Collector) attachZones(_ context.Context, snap *Snapshot) {
	if c.running == nil {
		return
	}
	snap.ZoneRead = c.zoneRead
	FillZones(snap, c.runningPods, c.nodeList, c.workloadList, c.meshNamespace, c.peerAuths, fluxReleaseKeys(c.fluxList))
}

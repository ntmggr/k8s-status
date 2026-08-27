package status

import (
	"context"
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

// EventLister reads scheduling failures. Optional, like the others: without it the page
// simply does not say why anything is pending.
type EventLister interface {
	ListFailedScheduling(ctx context.Context) (*kube.EventList, error)
}

// WorkloadLister is optional in the same way, for UNMANAGED. Listing workloads in
// every namespace needs a ClusterRole, so the default deployment never calls it.
type WorkloadLister interface {
	ListWorkloads(ctx context.Context) (*kube.WorkloadList, error)
}

// FluxLister is optional in the same way, for SOURCES. It is only wired up when flux is
// one of the enabled sources, so an ArgoCD-only deployment never calls the Flux APIs.
type FluxLister interface {
	ListFlux(ctx context.Context) (*kube.FluxList, error)
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
	events    EventLister
	workloads WorkloadLister
	flux      FluxLister

	nodeStats *NodeStats
	nodesAt   time.Time
	nodeList  *kube.NodeList

	events2  *kube.EventList
	eventsAt time.Time

	unmanaged    *Unmanaged
	unmanagedAt  time.Time
	workloadList *kube.WorkloadList
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

// WithEvents enables the "why is this pending" note. Not called means the events API is
// never queried and no row is ever marked blocked.
func (c *Collector) WithEvents(el EventLister) *Collector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = el
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
		var err error
		if list, err = c.lister.ListApplications(ctx); err != nil {
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
	}
	snap.CheckedAt = c.now()
	c.snap = &snap
	return c.decorate(ctx, snap), nil
}

// decorate returns a copy of the snapshot carrying the optional sections, so the
// cached snapshot is never mutated after callers have a pointer to it.
func (c *Collector) decorate(ctx context.Context, snap Snapshot) *Snapshot {
	c.attachNodes(ctx, &snap)
	c.attachUnmanaged(ctx, &snap)
	c.attachPending(ctx, &snap)
	return &snap
}

func (c *Collector) attachNodes(ctx context.Context, snap *Snapshot) {
	if c.nodes == nil {
		return
	}
	// A node list changes slowly, and a denied read is cached too so an operator who
	// enabled the flag without the ClusterRole does not hammer the API server.
	if c.nodeStats == nil || c.now().Sub(c.nodesAt) >= c.ttl {
		list, err := c.nodes.ListNodes(ctx)
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
	snap.Nodes = c.nodeStats
}

// attachPending explains pods the scheduler refused. A failed read is swallowed: the
// note is an extra, and losing it must not take the page down.
func (c *Collector) attachPending(ctx context.Context, snap *Snapshot) {
	if c.events == nil {
		return
	}
	if c.events2 == nil || c.now().Sub(c.eventsAt) >= c.ttl {
		list, err := c.events.ListFailedScheduling(ctx)
		if err != nil {
			list = &kube.EventList{}
		}
		c.events2 = list
		c.eventsAt = c.now()
	}
	FillPending(snap, c.events2)
}

func (c *Collector) attachUnmanaged(ctx context.Context, snap *Snapshot) {
	if c.workloads == nil {
		return
	}
	// Same TTL and same reasoning as the nodes read: workloads outside GitOps change
	// slowly, and a denied read is cached so the flag without the ClusterRole does not
	// hammer the API server.
	if c.unmanaged == nil || c.now().Sub(c.unmanagedAt) >= c.ttl {
		list, err := c.workloads.ListWorkloads(ctx)
		u := BuildUnmanaged(list, c.opts)
		if err != nil {
			// A partial read keeps the kinds that did succeed and notes the rest.
			u = unmanagedError(u, err)
		}
		c.unmanaged = &u
		c.unmanagedAt = c.now()
		c.workloadList = list
	}
	snap.Unmanaged = c.unmanaged
	// The same workload list also recovers app versions ArgoCD did not report, and
	// tells us which services actually ask for a GPU.
	FillMissingVersions(snap, c.workloadList)
	FillGPU(snap, c.workloadList, c.nodeList, DiscoverAccelerators(c.nodeList, c.opts.AcceleratorResources))
}

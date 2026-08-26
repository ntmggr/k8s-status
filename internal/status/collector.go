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
	workloads WorkloadLister
	flux      FluxLister

	nodeStats *NodeStats
	nodesAt   time.Time

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
			stats = BuildNodeStats(list)
		}
		c.nodeStats = &stats
		c.nodesAt = c.now()
	}
	snap.Nodes = c.nodeStats
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
	FillGPU(snap, c.workloadList)
}

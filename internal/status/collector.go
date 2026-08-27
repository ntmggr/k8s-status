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

// PendingLister reads the scheduler's placement failures. Optional, like the others:
// without it the page simply does not say why anything is pending.
type PendingLister interface {
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
	pending   PendingLister
	workloads WorkloadLister
	flux      FluxLister

	nodeStats *NodeStats
	nodesAt   time.Time
	nodeList  *kube.NodeList

	pendingPods *kube.EventList
	pendingAt   time.Time

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
	c.attachPending(ctx, &snap)
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
	if c.pending != nil && stale(c.pendingAt, c.pendingPods == nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fetchPending(ctx)
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
	u := BuildUnmanaged(list, c.opts)
	if err != nil {
		u = unmanagedError(u, err)
	}
	c.unmanaged = &u
	c.unmanagedAt = c.now()
	c.workloadList = list
}

func (c *Collector) fetchPending(ctx context.Context) {
	list, err := c.pending.ListFailedScheduling(ctx)
	if abandoned(err) {
		return
	}
	if err != nil {
		list = &kube.EventList{}
	}
	c.pendingPods = list
	c.pendingAt = c.now()
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
	FillPending(snap, c.pendingPods)
}

func (c *Collector) attachUnmanaged(_ context.Context, snap *Snapshot) {
	if c.workloads == nil {
		return
	}
	snap.Unmanaged = c.unmanaged
	// The same workload list recovers app versions ArgoCD did not report, says which
	// services actually ask for a device, and which are pinned to one architecture.
	FillMissingVersions(snap, c.workloadList)
	FillGPU(snap, c.workloadList, c.nodeList, DiscoverAccelerators(c.nodeList, c.opts.AcceleratorResources))
	FillArch(snap, c.workloadList, c.nodeList)
}

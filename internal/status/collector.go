package status

import (
	"context"
	"sync"
	"time"

	"github.com/ntmggr/srv-status/internal/argocd"
	"github.com/ntmggr/srv-status/internal/kube"
)

type Lister interface {
	ListApplications(ctx context.Context) (*argocd.ApplicationList, error)
}

// NodeLister is optional: it is only wired up when NODE_STATS is enabled, so the
// default deployment never touches the cluster-scoped nodes API.
type NodeLister interface {
	ListNodes(ctx context.Context) (*kube.NodeList, error)
}

// Collector caches one Snapshot for a TTL. The mutex is deliberately held across
// the upstream fetch so a burst of concurrent callers collapses into one request.
type Collector struct {
	lister Lister
	opts   Options
	ttl    time.Duration
	now    func() time.Time

	mu    sync.Mutex
	snap  *Snapshot
	nodes NodeLister

	nodeStats *NodeStats
	nodesAt   time.Time
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

// Get returns the cached snapshot, refreshing it when the TTL has expired.
// On a failed refresh it returns the last good snapshot marked stale, plus the error.
func (c *Collector) Get(ctx context.Context) (*Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.snap != nil && now.Sub(c.snap.CheckedAt) < c.ttl {
		return c.withNodes(ctx, *c.snap), nil
	}

	list, err := c.lister.ListApplications(ctx)
	if err != nil {
		if c.snap != nil {
			stale := *c.snap
			stale.Stale = true
			return c.withNodes(ctx, stale), err
		}
		return nil, err
	}

	snap := Build(list, c.opts)
	snap.CheckedAt = c.now()
	c.snap = &snap
	return c.withNodes(ctx, snap), nil
}

// withNodes returns a copy of the snapshot carrying the node stats, so the cached
// snapshot is never mutated after callers have a pointer to it.
func (c *Collector) withNodes(ctx context.Context, snap Snapshot) *Snapshot {
	if c.nodes == nil {
		return &snap
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
	return &snap
}

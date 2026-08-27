package status

import (
	"context"
	"testing"
	"time"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// cancellingNodeLister fails the first call as though the viewer navigated away, then
// succeeds. It also records whether it saw an already-cancelled context.
type cancellingNodeLister struct {
	calls    int
	sawDone  bool
	failWith error
}

func (l *cancellingNodeLister) ListNodes(ctx context.Context) (*kube.NodeList, error) {
	l.calls++
	if ctx.Err() != nil {
		l.sawDone = true
	}
	if l.calls == 1 && l.failWith != nil {
		return nil, l.failWith
	}
	n := kube.Node{}
	n.Status.NodeInfo.Architecture = "arm64"
	n.Status.Capacity = map[string]kube.Quantity{"cpu": "4"}
	return &kube.NodeList{Items: []kube.Node{n}}, nil
}

// A cancelled read must leave the cache alone. Storing it showed "node stats
// unavailable" to everyone for the rest of the interval because one viewer clicked away.
func TestCancelledReadDoesNotPoisonTheCache(t *testing.T) {
	nl := &cancellingNodeLister{failWith: context.Canceled}
	c := NewCollector(nil, Options{}, time.Minute).WithNodes(nl)

	c.fetchNodes(context.Background())
	if c.nodeStats != nil {
		t.Fatalf("a cancelled read was cached: %+v", c.nodeStats)
	}
	if !c.nodesAt.IsZero() {
		t.Fatal("a cancelled read must not refresh the timestamp, or it holds the gap open")
	}

	c.fetchNodes(context.Background())
	if c.nodeStats == nil || c.nodeStats.Total != 1 {
		t.Fatalf("the next read should populate the cache, got %+v", c.nodeStats)
	}
}

// A genuine failure is still cached, so a missing ClusterRole does not hammer the API.
func TestRealFailureIsStillCached(t *testing.T) {
	nl := &cancellingNodeLister{failWith: &kube.StatusError{Code: 403, Body: "forbidden"}}
	c := NewCollector(nil, Options{}, time.Minute).WithNodes(nl)

	c.fetchNodes(context.Background())
	if c.nodeStats == nil || c.nodeStats.Error == "" {
		t.Fatalf("a real error should be cached and shown, got %+v", c.nodeStats)
	}
}

// The refresh must survive the request that started it going away.
func TestRefreshSourcesDetachesFromTheRequest(t *testing.T) {
	nl := &cancellingNodeLister{}
	c := NewCollector(nil, Options{}, time.Minute).WithNodes(nl)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the viewer has already clicked away

	c.refreshSources(ctx)

	if nl.calls == 0 {
		t.Fatal("the read was skipped entirely because the request was cancelled")
	}
	if nl.sawDone {
		t.Fatal("the read inherited the cancelled request context")
	}
	if c.nodeStats == nil || c.nodeStats.Total != 1 {
		t.Fatalf("the refresh should have completed, got %+v", c.nodeStats)
	}
}

package status

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ntmggr/srv-status/internal/kube"
)

func decodeNodes(t *testing.T, raw string) *kube.NodeList {
	t.Helper()
	var list kube.NodeList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("decode node list: %v", err)
	}
	return &list
}

const nodeFixture = `{"items":[
 {"metadata":{"name":"gpu-a"},"status":{"capacity":{"cpu":"8","nvidia.com/gpu":"4"},"nodeInfo":{"architecture":"amd64"}}},
 {"metadata":{"name":"gpu-b"},"status":{"capacity":{"nvidia.com/gpu":"1"},"nodeInfo":{"architecture":"amd64"}}},
 {"metadata":{"name":"cpu-a"},"status":{"capacity":{"cpu":"4"},"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-b"},"status":{"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-c"},"status":{"capacity":{"nvidia.com/gpu":"0"},"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-d"},"status":{"capacity":{"nvidia.com/gpu":"not-a-number"},"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-e"},"status":{"capacity":{"nvidia.com/gpu":null},"nodeInfo":{}}}
]}`

func TestBuildNodeStatsClassifiesAndSums(t *testing.T) {
	got := BuildNodeStats(decodeNodes(t, nodeFixture))

	if got.Total != 7 {
		t.Errorf("total = %d, want 7", got.Total)
	}
	if got.GPUNodes != 2 {
		t.Errorf("gpu nodes = %d, want 2", got.GPUNodes)
	}
	if got.CPUNodes != 5 {
		t.Errorf("cpu nodes = %d, want 5", got.CPUNodes)
	}
	if got.GPUs != 5 {
		t.Errorf("gpus = %d, want 5", got.GPUs)
	}
	if got.CPUNodes+got.GPUNodes != got.Total {
		t.Errorf("cpu+gpu = %d, want %d", got.CPUNodes+got.GPUNodes, got.Total)
	}
}

func TestBuildNodeStatsArchTally(t *testing.T) {
	got := BuildNodeStats(decodeNodes(t, nodeFixture))

	want := []ArchCount{{Arch: "arm64", Count: 4}, {Arch: "amd64", Count: 2}, {Arch: archUnknown, Count: 1}}
	if len(got.Arch) != len(want) {
		t.Fatalf("arch = %+v, want %+v", got.Arch, want)
	}
	for i := range want {
		if got.Arch[i] != want[i] {
			t.Errorf("arch[%d] = %+v, want %+v", i, got.Arch[i], want[i])
		}
	}
}

func TestBuildNodeStatsAcceptsBareNumberQuantity(t *testing.T) {
	got := BuildNodeStats(decodeNodes(t,
		`{"items":[{"metadata":{"name":"g"},"status":{"capacity":{"nvidia.com/gpu":2},"nodeInfo":{"architecture":"amd64"}}}]}`))

	if got.GPUNodes != 1 || got.GPUs != 2 {
		t.Errorf("gpuNodes = %d, gpus = %d, want 1 and 2", got.GPUNodes, got.GPUs)
	}
}

func TestBuildNodeStatsEmptyAndNil(t *testing.T) {
	if got := BuildNodeStats(nil); got.Total != 0 || len(got.Arch) != 0 {
		t.Errorf("nil list = %+v", got)
	}
	if got := BuildNodeStats(&kube.NodeList{}); got.Total != 0 || len(got.Arch) != 0 {
		t.Errorf("empty list = %+v", got)
	}
}

type fakeNodeLister struct {
	called int
	list   *kube.NodeList
	err    error
}

func (f *fakeNodeLister) ListNodes(context.Context) (*kube.NodeList, error) {
	f.called++
	return f.list, f.err
}

// The default deployment holds no ClusterRole, so a collector without WithNodes must
// never reach for the cluster-scoped nodes API.
func TestNodesNotFetchedWhenDisabled(t *testing.T) {
	nodeLister := &fakeNodeLister{list: decodeNodes(t, nodeFixture)}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Nodes != nil {
		t.Errorf("Nodes = %+v, want nil when the feature is off", snap.Nodes)
	}
	if nodeLister.called != 0 {
		t.Errorf("nodes API called %d times, want 0", nodeLister.called)
	}
}

func TestNodesFetchedOncePerTTL(t *testing.T) {
	nodeLister := &fakeNodeLister{list: decodeNodes(t, nodeFixture)}
	c, clock := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithNodes(nodeLister)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Nodes == nil || snap.Nodes.Total != 7 {
		t.Fatalf("Nodes = %+v", snap.Nodes)
	}

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if nodeLister.called != 1 {
		t.Errorf("nodes fetches within TTL = %d, want 1", nodeLister.called)
	}

	clock.advance(16 * time.Second)
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("refresh get: %v", err)
	}
	if nodeLister.called != 2 {
		t.Errorf("nodes fetches after TTL = %d, want 2", nodeLister.called)
	}
}

func TestNodesDeniedDegradesInsteadOfFailing(t *testing.T) {
	nodeLister := &fakeNodeLister{err: &kube.StatusError{Code: http.StatusForbidden, Body: `nodes is forbidden`}}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithNodes(nodeLister)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("a denied nodes read must not fail the snapshot: %v", err)
	}
	if snap.Summary.Total != 14 {
		t.Errorf("application data lost: total = %d, want 14", snap.Summary.Total)
	}
	if snap.Nodes == nil {
		t.Fatal("want a NodeStats carrying the error")
	}
	if !snap.Nodes.Denied {
		t.Error("403 should be reported as denied")
	}
	if snap.Nodes.Error == "" {
		t.Error("want the error text surfaced in the section")
	}
}

func TestNodesTransportErrorIsNotDenied(t *testing.T) {
	nodeLister := &fakeNodeLister{err: errors.New("query kubernetes api: connection refused")}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithNodes(nodeLister)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Nodes.Denied {
		t.Error("a transport error is not an RBAC denial")
	}
}

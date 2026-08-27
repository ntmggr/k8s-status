package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// The collector decorates a shallow copy of the cached snapshot, so these run twice
// over the same rows. Totals must not accumulate.
func TestFillsAreIdempotent(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("svc", "ml", "worker"))
	wl := &kube.WorkloadList{Items: []kube.Workload{replicaWorkload("Deployment", "ml", "worker", 1, 2, 2)}}
	ev := &kube.EventList{Items: []kube.Event{schedEvent("ml", "worker-1-a", "0/5 nodes are available: 5 Insufficient cpu.")}}

	for i := 0; i < 3; i++ {
		FillGPU(snap, wl, nil, []string{kube.ResourceGPU})
		FillPending(snap, ev)
	}

	g := snap.Services[0].GPUAlloc
	if g.PerReplica != 1 || g.Desired != 2 || g.Ready != 2 || g.Allocated != 2 {
		t.Errorf("GPUAlloc accumulated: %+v", g)
	}
	if snap.Summary.Blocked != 1 {
		t.Errorf("Summary.Blocked=%d, want 1", snap.Summary.Blocked)
	}
	if snap.Services[0].Blocked.Pods != 1 {
		t.Errorf("Blocked.Pods=%d, want 1", snap.Services[0].Blocked.Pods)
	}
}

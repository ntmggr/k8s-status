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
	ev := &kube.PodList{Items: []kube.Pod{pendingPod("ml", "worker-1-a", "0/5 nodes are available: 5 Insufficient cpu.")}}

	nodes := &kube.NodeList{Items: []kube.Node{
		zonedNode("n1", "eu-west-1a", "10.0.1.1"),
		zonedNode("n2", "eu-west-1b", "10.0.1.2"),
	}}
	injected := runningZonedPod("ml", "worker-1", "10.0.1.1")
	injected.Status.ContainerStatuses = []kube.ContainerStatus{{Name: "istio-proxy"}}
	running := &kube.PodList{Items: []kube.Pod{
		injected,
		runningZonedPod("ml", "worker-2", "10.0.1.2"),
	}}

	for i := 0; i < 3; i++ {
		FillGPU(snap, wl, nil, []string{kube.ResourceGPU})
		FillPending(snap, ev, nil)
		FillZones(snap, running, nodes, nil, "")
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
	if snap.Summary.SingleZone != 0 || snap.Summary.MultiZone != 1 {
		t.Errorf("SingleZone/MultiZone = %d/%d, want 0/1", snap.Summary.SingleZone, snap.Summary.MultiZone)
	}
	z := snap.Services[0].Zones
	if z.Pods != 2 || z.Count() != 2 {
		t.Errorf("Zones accumulated: %+v", z)
	}
	if snap.Summary.SingleNode != 0 || snap.Summary.MultiNode != 1 {
		t.Errorf("SingleNode/MultiNode = %d/%d, want 0/1", snap.Summary.SingleNode, snap.Summary.MultiNode)
	}
	n := snap.Services[0].Nodes
	if n.Pods != 2 || n.Count() != 2 {
		t.Errorf("Nodes accumulated: %+v", n)
	}
	if snap.Summary.MeshEligible != 1 || snap.Summary.MeshInjected != 0 {
		t.Errorf("MeshEligible/MeshInjected = %d/%d, want 1/0 (one of two pods injected)", snap.Summary.MeshEligible, snap.Summary.MeshInjected)
	}
	m := snap.Services[0].Mesh
	if m.Pods != 2 || m.Injected != 1 {
		t.Errorf("Mesh accumulated: %+v", m)
	}
}

// zonedNode builds a node with one internal IP and a zone label, for FillZones tests.
func zonedNode(name, zone, ip string) kube.Node {
	var n kube.Node
	n.Metadata.Name = name
	n.Metadata.Labels = map[string]string{kube.LabelZone: zone}
	n.Status.Addresses = []kube.NodeAddress{{Type: "InternalIP", Address: ip}}
	return n
}

// runningZonedPod builds a Running pod owned by "worker", landed on the node at hostIP.
func runningZonedPod(ns, name, hostIP string) kube.Pod {
	var p kube.Pod
	p.Metadata.Namespace, p.Metadata.Name = ns, name
	p.Metadata.OwnerReferences = []kube.OwnerReference{{Kind: "Deployment", Name: "worker"}}
	p.Status.Phase = "Running"
	p.Status.HostIP = hostIP
	return p
}

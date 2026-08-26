package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// Most tests describe an NVIDIA cluster; discovery is exercised separately.
var nvidiaOnly = []string{kube.ResourceGPU}

func gpuWorkload(kind, ns, name string, limit, request string) kube.Workload {
	c := kube.Container{Image: "example/img:1"}
	if limit != "" {
		c.Resources.Limits = map[string]string{kube.ResourceGPU: limit}
	}
	if request != "" {
		c.Resources.Requests = map[string]string{kube.ResourceGPU: request}
	}
	w := kube.Workload{Kind: kind}
	w.Metadata.Namespace, w.Metadata.Name = ns, name
	w.Spec.Template.Spec.Containers = []kube.Container{c}
	return w
}

func TestFillGPUMarksOnlyRealRequests(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "inference", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "inference"}}})
	snap.addService(Service{Name: "api", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "api"}}})
	// Named like a GPU service but asks for nothing: the case name globs got wrong.
	snap.addService(Service{Name: "gpu-lookalike", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "lookalike"}}})

	list := &kube.WorkloadList{Items: []kube.Workload{
		gpuWorkload("Deployment", "ml", "inference", "1", ""),
		gpuWorkload("Deployment", "ml", "api", "", ""),
		gpuWorkload("Deployment", "ml", "lookalike", "", ""),
	}}

	FillGPU(snap, list, nil, nvidiaOnly)

	want := map[string]bool{"inference": true, "api": false, "gpu-lookalike": false}
	for _, svc := range snap.Services {
		if svc.GPU != want[svc.Name] {
			t.Errorf("%s: GPU=%v, want %v", svc.Name, svc.GPU, want[svc.Name])
		}
	}
	if snap.Summary.GPU != 1 {
		t.Errorf("Summary.GPU=%d, want 1", snap.Summary.GPU)
	}
}

// A request without a limit still counts, and a service scaled to zero is still a GPU
// service: detection reads the spec, not running pods.
func TestFillGPURequestOnlyAndScaledToZero(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "batch", Owned: []Component{{Kind: "StatefulSet", Namespace: "ml", Name: "batch"}}})

	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{
		gpuWorkload("StatefulSet", "ml", "batch", "", "2"),
	}}, nil, nvidiaOnly)

	if !snap.Services[0].GPU || snap.Summary.GPU != 1 {
		t.Fatalf("GPU=%v summary=%d, want true/1", snap.Services[0].GPU, snap.Summary.GPU)
	}
}

// Kind and namespace both have to match: two workloads may share a name.
func TestFillGPUDoesNotMatchAcrossNamespaceOrKind(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "svc", Owned: []Component{{Kind: "Deployment", Namespace: "a", Name: "same"}}})

	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{
		gpuWorkload("Deployment", "b", "same", "1", ""),  // other namespace
		gpuWorkload("StatefulSet", "a", "same", "1", ""), // other kind
	}}, nil, nvidiaOnly)

	if snap.Services[0].GPU || snap.Summary.GPU != 0 {
		t.Fatalf("GPU=%v summary=%d, want false/0", snap.Services[0].GPU, snap.Summary.GPU)
	}
}

// Without the workload list nothing is claimed, and a glob-set flag is left alone.
func TestFillGPUNilListLeavesGlobResultIntact(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "flagged-by-glob", GPU: true})
	before := snap.Summary.GPU

	FillGPU(snap, nil, nil, nvidiaOnly)

	if !snap.Services[0].GPU || snap.Summary.GPU != before {
		t.Fatalf("nil list must not change anything: GPU=%v summary=%d", snap.Services[0].GPU, snap.Summary.GPU)
	}
}

func TestWorkloadGPUsSumsContainersAndIgnoresJunk(t *testing.T) {
	w := kube.Workload{Kind: "Deployment"}
	w.Spec.Template.Spec.Containers = []kube.Container{
		{Resources: kube.ResourceRequirements{Limits: map[string]string{kube.ResourceGPU: "2"}}},
		{Resources: kube.ResourceRequirements{Limits: map[string]string{kube.ResourceGPU: "1"}}},
		{Resources: kube.ResourceRequirements{Limits: map[string]string{kube.ResourceGPU: "not-a-number"}}},
		{Resources: kube.ResourceRequirements{Limits: map[string]string{"cpu": "500m"}}},
	}
	if got := workloadGPUs(w, nvidiaOnly); got != 3 {
		t.Fatalf("workloadGPUs=%d, want 3", got)
	}
}

func node(name string, gpus string, labels map[string]string) kube.Node {
	var n kube.Node
	n.Metadata.Name, n.Metadata.Labels = name, labels
	n.Status.Capacity = map[string]kube.Quantity{kube.ResourceGPU: kube.Quantity(gpus)}
	return n
}

func affinityTo(w kube.Workload, key string, values ...string) kube.Workload {
	w.Spec.Template.Spec.Affinity = &kube.Affinity{
		NodeAffinity: &kube.NodeAffinity{
			Required: &kube.NodeSelectorSpec{
				NodeSelectorTerms: []kube.NodeSelectorTerm{{
					MatchExpressions: []kube.NodeSelectorRequirement{
						{Key: key, Operator: "In", Values: values},
					},
				}},
			},
		},
	}
	return w
}

// The case that name globs used to cover and resource requests miss: a workload
// pinned to a GPU nodegroup that never names nvidia.com/gpu.
func TestFillGPUDetectsAffinityToGPUNodes(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{
		node("gpu-1", "1", map[string]string{"NodeGroupType": "inference"}),
		node("gpu-2", "1", map[string]string{"NodeGroupType": "inference"}),
		node("cpu-1", "0", map[string]string{"NodeGroupType": "general"}),
	}}
	snap := &Snapshot{}
	snap.addService(Service{Name: "pinned", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "pinned"}}})

	w := affinityTo(gpuWorkload("Deployment", "ml", "pinned", "", ""), "NodeGroupType", "inference")
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nodes, nvidiaOnly)

	if !snap.Services[0].GPU || snap.Summary.GPU != 1 {
		t.Fatalf("GPU=%v summary=%d, want true/1", snap.Services[0].GPU, snap.Summary.GPU)
	}
}

// Pinned to a nodegroup that has no GPUs: still not a GPU service, however it is named.
func TestFillGPUAffinityToCPUNodesStaysFalse(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{
		node("gpu-1", "1", map[string]string{"NodeGroupType": "inference"}),
		node("cpu-1", "0", map[string]string{"NodeGroupType": "transcode"}),
	}}
	snap := &Snapshot{}
	snap.addService(Service{Name: "transcoder", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "transcoder"}}})

	w := affinityTo(gpuWorkload("Deployment", "ml", "transcoder", "", ""), "NodeGroupType", "transcode")
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nodes, nvidiaOnly)

	if snap.Services[0].GPU || snap.Summary.GPU != 0 {
		t.Fatalf("GPU=%v summary=%d, want false/0", snap.Services[0].GPU, snap.Summary.GPU)
	}
}

// An unconstrained workload can land anywhere, so placement proves nothing. This is
// what keeps DaemonSets from being claimed as GPU workloads.
func TestFillGPUUnconstrainedWorkloadIsNotGPU(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{
		node("gpu-1", "1", map[string]string{"NodeGroupType": "inference"}),
		node("cpu-1", "0", map[string]string{"NodeGroupType": "general"}),
	}}
	snap := &Snapshot{}
	snap.addService(Service{Name: "agent", Owned: []Component{{Kind: "DaemonSet", Namespace: "ops", Name: "agent"}}})

	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{
		gpuWorkload("DaemonSet", "ops", "agent", "", ""),
	}}, nodes, nvidiaOnly)

	if snap.Services[0].GPU {
		t.Fatal("an unconstrained DaemonSet must not be reported as GPU-backed")
	}
}

// nodeSelector is the older spelling of the same constraint and must work too.
func TestFillGPUNodeSelectorCounts(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{
		node("gpu-1", "2", map[string]string{"pool": "gpu"}),
		node("cpu-1", "0", map[string]string{"pool": "cpu"}),
	}}
	snap := &Snapshot{}
	snap.addService(Service{Name: "svc", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "svc"}}})

	w := gpuWorkload("Deployment", "ml", "svc", "", "")
	w.Spec.Template.Spec.NodeSelector = map[string]string{"pool": "gpu"}
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nodes, nvidiaOnly)

	if !snap.Services[0].GPU {
		t.Fatal("nodeSelector pinning to GPU nodes should be detected")
	}
}

// Without a node list the placement signal is simply unavailable; nothing is invented.
func TestFillGPUNoNodesFallsBackToRequestsOnly(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "pinned", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "pinned"}}})

	w := affinityTo(gpuWorkload("Deployment", "ml", "pinned", "", ""), "NodeGroupType", "inference")
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nil, nvidiaOnly)

	if snap.Services[0].GPU {
		t.Fatal("without nodes, placement cannot be evaluated and must not be claimed")
	}
}

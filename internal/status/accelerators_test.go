package status

import (
	"reflect"
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func capNode(cap map[string]string) kube.Node {
	var n kube.Node
	n.Status.Capacity = map[string]kube.Quantity{}
	for k, v := range cap {
		n.Status.Capacity[k] = kube.Quantity(v)
	}
	return n
}

func TestDiscoverAccelerators(t *testing.T) {
	cases := []struct {
		name  string
		nodes []kube.Node
		want  []string
	}{
		{
			name:  "core resources are never accelerators",
			nodes: []kube.Node{capNode(map[string]string{"cpu": "4", "memory": "16Gi", "pods": "58", "ephemeral-storage": "100Gi"})},
			want:  nil,
		},
		{
			name:  "nvidia full cards",
			nodes: []kube.Node{capNode(map[string]string{"cpu": "4", "nvidia.com/gpu": "2"})},
			want:  []string{"nvidia.com/gpu"},
		},
		{
			// The case that reads zero GPUs today: a MIG-partitioned node advertises
			// slices and no nvidia.com/gpu at all.
			name:  "nvidia MIG slices",
			nodes: []kube.Node{capNode(map[string]string{"cpu": "8", "nvidia.com/mig-1g.5gb": "7"})},
			want:  []string{"nvidia.com/mig-1g.5gb"},
		},
		{
			name: "other vendors",
			nodes: []kube.Node{
				capNode(map[string]string{"amd.com/gpu": "1"}),
				capNode(map[string]string{"aws.amazon.com/neuron": "4"}),
				capNode(map[string]string{"gpu.intel.com/i915": "1"}),
			},
			want: []string{"amd.com/gpu", "aws.amazon.com/neuron", "gpu.intel.com/i915"},
		},
		{
			// EKS advertises these when security groups for pods are on. Counting them
			// would mark every node in such a cluster as accelerated.
			name:  "EKS network resources are excluded",
			nodes: []kube.Node{capNode(map[string]string{"vpc.amazonaws.com/pod-eni": "9", "cpu": "4"})},
			want:  nil,
		},
		{
			name:  "hugepages are excluded",
			nodes: []kube.Node{capNode(map[string]string{"hugepages-2Mi": "0", "hugepages-1Gi": "4"})},
			want:  nil,
		},
		{
			name:  "attachable volumes have no vendor domain",
			nodes: []kube.Node{capNode(map[string]string{"attachable-volumes-aws-ebs": "25"})},
			want:  nil,
		},
		{
			name:  "a resource advertised as zero is not reported",
			nodes: []kube.Node{capNode(map[string]string{"nvidia.com/gpu": "0"})},
			want:  nil,
		},
		{
			name: "mixed cluster is sorted and deduplicated",
			nodes: []kube.Node{
				capNode(map[string]string{"nvidia.com/gpu": "1"}),
				capNode(map[string]string{"nvidia.com/gpu": "1"}),
				capNode(map[string]string{"amd.com/gpu": "2"}),
			},
			want: []string{"amd.com/gpu", "nvidia.com/gpu"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DiscoverAccelerators(&kube.NodeList{Items: tc.nodes}, nil)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiscoverAcceleratorsOverrideWins(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{capNode(map[string]string{"nvidia.com/gpu": "1"})}}
	got := DiscoverAccelerators(nodes, []string{"vpc.amazonaws.com/pod-eni"})
	if !reflect.DeepEqual(got, []string{"vpc.amazonaws.com/pod-eni"}) {
		t.Fatalf("override must replace discovery, got %v", got)
	}
}

func TestBuildNodeStatsCountsEveryAcceleratorKind(t *testing.T) {
	list := &kube.NodeList{Items: []kube.Node{
		capNode(map[string]string{"nvidia.com/gpu": "2"}),
		capNode(map[string]string{"nvidia.com/mig-1g.5gb": "7"}),
		capNode(map[string]string{"cpu": "4"}),
	}}
	got := BuildNodeStats(list, DiscoverAccelerators(list, nil))

	if got.Total != 3 || got.GPUNodes != 2 || got.CPUNodes != 1 {
		t.Fatalf("total=%d gpuNodes=%d cpuNodes=%d, want 3/2/1", got.Total, got.GPUNodes, got.CPUNodes)
	}
	if got.GPUs != 9 { // 2 cards + 7 slices
		t.Fatalf("GPUs=%d, want 9", got.GPUs)
	}
	want := []AcceleratorCount{
		{Resource: "nvidia.com/gpu", Nodes: 1, Count: 2},
		{Resource: "nvidia.com/mig-1g.5gb", Nodes: 1, Count: 7},
	}
	if !reflect.DeepEqual(got.Accelerators, want) {
		t.Fatalf("breakdown = %+v, want %+v", got.Accelerators, want)
	}
}

// A MIG-only cluster used to report zero GPUs everywhere, because only the literal
// nvidia.com/gpu name was ever matched.
func TestFillGPUDetectsNonNvidiaRequests(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{capNode(map[string]string{"aws.amazon.com/neuron": "4"})}}
	accel := DiscoverAccelerators(nodes, nil)

	snap := &Snapshot{}
	snap.addService(Service{Name: "inference", Owned: []Component{{Kind: "Deployment", Namespace: "ml", Name: "inference"}}})

	w := kube.Workload{Kind: "Deployment"}
	w.Metadata.Namespace, w.Metadata.Name = "ml", "inference"
	w.Spec.Template.Spec.Containers = []kube.Container{{
		Resources: kube.ResourceRequirements{Limits: map[string]string{"aws.amazon.com/neuron": "1"}},
	}}

	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nodes, accel)

	if !snap.Services[0].GPU || snap.Summary.GPU != 1 {
		t.Fatalf("neuron request not detected: GPU=%v summary=%d", snap.Services[0].GPU, snap.Summary.GPU)
	}
}

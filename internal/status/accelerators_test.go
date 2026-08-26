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

func snapWith(accs ...AcceleratorCount) *Snapshot {
	s := &Snapshot{}
	if accs != nil {
		s.Nodes = &NodeStats{Accelerators: accs}
	}
	return s
}

func TestAcceleratorLabels(t *testing.T) {
	cases := []struct {
		name                       string
		snap                       *Snapshot
		label, badge, unit, detail string
	}{
		{
			// No node stats: the page still marks services from resource requests, so
			// it needs a word, and "gpu" is the one that is right almost everywhere.
			name: "no node stats falls back to gpu",
			snap: &Snapshot{}, label: "gpu", badge: "GPU", unit: "cards", detail: "",
		},
		{
			name:  "nvidia cards",
			snap:  snapWith(AcceleratorCount{Resource: "nvidia.com/gpu", Nodes: 8, Count: 14}),
			label: "gpu", badge: "GPU", unit: "cards", detail: "nvidia.com/gpu 14",
		},
		{
			name:  "amd is still a gpu",
			snap:  snapWith(AcceleratorCount{Resource: "amd.com/gpu", Nodes: 2, Count: 2}),
			label: "gpu", badge: "GPU", unit: "cards", detail: "amd.com/gpu 2",
		},
		{
			// A MIG slice is not a card, so the unit changes with the noun.
			name:  "mig slices",
			snap:  snapWith(AcceleratorCount{Resource: "nvidia.com/mig-1g.5gb", Nodes: 1, Count: 7}),
			label: "mig", badge: "MIG", unit: "devices", detail: "nvidia.com/mig-1g.5gb 7",
		},
		{
			name:  "neuron",
			snap:  snapWith(AcceleratorCount{Resource: "aws.amazon.com/neuron", Nodes: 3, Count: 12}),
			label: "neuron", badge: "NEURON", unit: "devices", detail: "aws.amazon.com/neuron 12",
		},
		{
			name:  "tpu",
			snap:  snapWith(AcceleratorCount{Resource: "google.com/tpu", Nodes: 4, Count: 32}),
			label: "tpu", badge: "TPU", unit: "devices", detail: "google.com/tpu 32",
		},
		{
			// More than one kind has no single honest noun, and the badge has to stay
			// short enough to sit beside a service name.
			name: "mixed cluster",
			snap: snapWith(
				AcceleratorCount{Resource: "amd.com/gpu", Nodes: 1, Count: 1},
				AcceleratorCount{Resource: "nvidia.com/gpu", Nodes: 2, Count: 4},
			),
			label: "accelerators", badge: "ACCEL", unit: "devices",
			detail: "amd.com/gpu 1, nvidia.com/gpu 4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.snap.AcceleratorLabel(); got != tc.label {
				t.Errorf("label = %q, want %q", got, tc.label)
			}
			if got := tc.snap.AcceleratorBadge(); got != tc.badge {
				t.Errorf("badge = %q, want %q", got, tc.badge)
			}
			if got := tc.snap.AcceleratorUnit(); got != tc.unit {
				t.Errorf("unit = %q, want %q", got, tc.unit)
			}
			if got := tc.snap.AcceleratorDetail(); got != tc.detail {
				t.Errorf("detail = %q, want %q", got, tc.detail)
			}
		})
	}
}

// A nil Snapshot must not panic: the template calls these on every render.
func TestAcceleratorLabelNilSnapshot(t *testing.T) {
	var s *Snapshot
	if got := s.AcceleratorLabel(); got != "gpu" {
		t.Fatalf("label = %q, want gpu", got)
	}
	if got := s.AcceleratorDetail(); got != "" {
		t.Fatalf("detail = %q, want empty", got)
	}
}

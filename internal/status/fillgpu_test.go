package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

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

	FillGPU(snap, list)

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
	}})

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
	}})

	if snap.Services[0].GPU || snap.Summary.GPU != 0 {
		t.Fatalf("GPU=%v summary=%d, want false/0", snap.Services[0].GPU, snap.Summary.GPU)
	}
}

// Without the workload list nothing is claimed, and a glob-set flag is left alone.
func TestFillGPUNilListLeavesGlobResultIntact(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "flagged-by-glob", GPU: true})
	before := snap.Summary.GPU

	FillGPU(snap, nil)

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
	if got := workloadGPUs(w); got != 3 {
		t.Fatalf("workloadGPUs=%d, want 3", got)
	}
}

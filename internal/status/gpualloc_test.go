package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func TestGPUNameOnlyFallbackDoesNotInventZeroReplicas(t *testing.T) {
	snap := &Snapshot{Services: []Service{{Name: "gpu-api", GPU: true}}, Summary: Summary{GPU: 1}}
	FillGPU(snap, &kube.WorkloadList{}, nil, nil)
	if snap.Services[0].GPUAlloc.Known || snap.Services[0].GPUAlloc.State() != gpuStateUnknown ||
		snap.Summary.GPUUnknown() != 1 || snap.Summary.GPUStopped != 0 {
		t.Fatalf("missing workload data must stay unknown: %+v", snap.Summary)
	}
}

func replicaWorkload(kind, ns, name string, gpuPerReplica, desired, ready int) kube.Workload {
	w := kube.Workload{Kind: kind}
	w.Metadata.Namespace, w.Metadata.Name = ns, name
	c := kube.Container{Image: "example/img:1"}
	if gpuPerReplica > 0 {
		c.Resources.Limits = map[string]string{kube.ResourceGPU: itoa(gpuPerReplica)}
	}
	w.Spec.Template.Spec.Containers = []kube.Container{c}
	if kind == "DaemonSet" {
		w.Status.DesiredNumberScheduled, w.Status.NumberReady = desired, ready
	} else {
		d := desired
		w.Spec.Replicas = &d
		w.Status.Replicas, w.Status.ReadyReplicas = desired, ready
	}
	return w
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func svcWith(name, ns, wl string) Service {
	return Service{Name: name, Owned: []Component{{Kind: "Deployment", Namespace: ns, Name: wl}}}
}

func TestGPUAllocationStates(t *testing.T) {
	cases := []struct {
		name                         string
		gpu, desired, ready          int
		wantAlloc                    int
		waiting, stopped, unmeasured bool
		wantState                    string
	}{
		{name: "holding devices", gpu: 1, desired: 1, ready: 1, wantAlloc: 1, wantState: gpuStateRunning},
		{name: "several per replica", gpu: 3, desired: 2, ready: 2, wantAlloc: 6, wantState: gpuStateRunning},
		// Fewer replicas are ready than requested; the cause is not established here.
		{name: "unready replicas", gpu: 1, desired: 2, ready: 0, wantAlloc: 0, waiting: true, wantState: gpuStateWaiting},
		{name: "partially rolled out", gpu: 1, desired: 3, ready: 1, wantAlloc: 1, waiting: true, wantState: gpuStateWaiting},
		// No desired replicas; it may still be terminating.
		{name: "parked at zero", gpu: 1, desired: 0, ready: 0, wantAlloc: 0, stopped: true, wantState: gpuStateScaledToZero},
		// Reaches the device through the runtime; declared requests are absent.
		{name: "no request at all", gpu: 0, desired: 2, ready: 2, wantAlloc: 0, unmeasured: true, wantState: gpuStateRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &Snapshot{}
			snap.addService(svcWith("svc", "ml", "svc"))
			nodes := &kube.NodeList{Items: []kube.Node{node("gpu-1", "4", map[string]string{"k": "v"})}}
			w := replicaWorkload("Deployment", "ml", "svc", tc.gpu, tc.desired, tc.ready)
			if tc.gpu == 0 {
				// Force it to be recognised as GPU-backed by placement instead.
				w = affinityTo(w, "k", "v")
			}
			FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nodes, []string{kube.ResourceGPU})

			g := snap.Services[0].GPUAlloc
			if !snap.Services[0].GPU {
				t.Fatal("service should be GPU-backed")
			}
			if g.Allocated != tc.wantAlloc {
				t.Errorf("Allocated=%d, want %d", g.Allocated, tc.wantAlloc)
			}
			if g.State() != tc.wantState {
				t.Errorf("State()=%q, want %q", g.State(), tc.wantState)
			}
			if g.Waiting() != tc.waiting {
				t.Errorf("Waiting=%v, want %v", g.Waiting(), tc.waiting)
			}
			if g.ScaledToZero() != tc.stopped {
				t.Errorf("ScaledToZero=%v, want %v", g.ScaledToZero(), tc.stopped)
			}
			if g.Unmeasured() != tc.unmeasured {
				t.Errorf("Unmeasured=%v, want %v", g.Unmeasured(), tc.unmeasured)
			}
		})
	}
}

// A service owning several workloads reports their sum, not the first one found.
func TestGPUAllocationSumsAcrossOwnedWorkloads(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "multi", Owned: []Component{
		{Kind: "Deployment", Namespace: "ml", Name: "a"},
		{Kind: "Deployment", Namespace: "ml", Name: "b"},
	}})
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{
		replicaWorkload("Deployment", "ml", "a", 1, 1, 1),
		replicaWorkload("Deployment", "ml", "b", 2, 2, 2),
	}}, nil, []string{kube.ResourceGPU})

	g := snap.Services[0].GPUAlloc
	if g.Allocated != 5 { // 1*1 + 2*2
		t.Fatalf("Allocated=%d, want 5", g.Allocated)
	}
	if g.Desired != 3 || g.Ready != 3 {
		t.Fatalf("desired/ready = %d/%d, want 3/3", g.Desired, g.Ready)
	}
}

// spec.replicas is the intent. status.replicas lags a scale-down and would make a
// parked service look like it is still trying to get a device.
func TestGPUAllocationUsesSpecReplicasNotStatus(t *testing.T) {
	w := replicaWorkload("Deployment", "ml", "svc", 1, 0, 0)
	w.Status.Replicas = 3 // still draining
	snap := &Snapshot{}
	snap.addService(svcWith("svc", "ml", "svc"))
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nil, []string{kube.ResourceGPU})

	if g := snap.Services[0].GPUAlloc; !g.ScaledToZero() || g.Waiting() {
		t.Fatalf("a draining scale-down must read as idle, got desired=%d ready=%d", g.Desired, g.Ready)
	}
}

func TestSummaryCountsUnreadyAndZeroTarget(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(svcWith("holding", "ml", "holding"))
	snap.addService(svcWith("stuck", "ml", "stuck"))
	snap.addService(svcWith("parked", "ml", "parked"))
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{
		replicaWorkload("Deployment", "ml", "holding", 1, 1, 1),
		replicaWorkload("Deployment", "ml", "stuck", 2, 1, 0),
		replicaWorkload("Deployment", "ml", "parked", 1, 0, 0),
	}}, nil, []string{kube.ResourceGPU})

	if snap.Summary.GPU != 3 {
		t.Fatalf("GPU=%d, want 3", snap.Summary.GPU)
	}
	if snap.Summary.GPUWaiting != 1 {
		t.Errorf("GPUWaiting=%d, want 1", snap.Summary.GPUWaiting)
	}
	if snap.Summary.GPUStopped != 1 {
		t.Errorf("GPUStopped=%d, want 1", snap.Summary.GPUStopped)
	}
}

// The request total includes declared requests, not unmeasured runtime access.
func TestSummaryCountsUnmeasuredServices(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{node("gpu-1", "4", map[string]string{"k": "v"})}}
	snap := &Snapshot{}
	snap.addService(svcWith("asks", "ml", "asks"))
	snap.addService(svcWith("just-pinned", "ml", "just-pinned"))

	pinned := affinityTo(replicaWorkload("Deployment", "ml", "just-pinned", 0, 1, 1), "k", "v")
	FillGPU(snap, &kube.WorkloadList{Items: []kube.Workload{
		replicaWorkload("Deployment", "ml", "asks", 2, 1, 1),
		pinned,
	}}, nodes, []string{kube.ResourceGPU})

	if snap.Summary.GPUUnmeasured != 1 {
		t.Errorf("GPUUnmeasured=%d, want 1", snap.Summary.GPUUnmeasured)
	}
	// No desired replicas does not prove there are no terminating pods during
	// scale-down, so a runtime-access workload remains unmeasured.
	snap2 := &Snapshot{}
	snap2.addService(svcWith("parked", "ml", "parked"))
	FillGPU(snap2, &kube.WorkloadList{Items: []kube.Workload{
		affinityTo(replicaWorkload("Deployment", "ml", "parked", 0, 0, 0), "k", "v"),
	}}, nodes, []string{kube.ResourceGPU})
	if snap2.Summary.GPUUnmeasured != 1 {
		t.Errorf("runtime-access workload should remain unmeasured during scale-down: %d", snap2.Summary.GPUUnmeasured)
	}
	if snap.Nodes != nil {
		t.Fatal("guard: this test does not attach node stats")
	}
	// The measured one still contributes; the pinned one cannot.
	var total int
	for _, s := range snap.Services {
		total += s.GPUAlloc.Allocated
	}
	if total != 2 {
		t.Errorf("allocated total=%d, want 2 from the measured service only", total)
	}
}

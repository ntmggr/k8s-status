package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func pendingPod(ns, pod, msg string) kube.Pod {
	var p kube.Pod
	p.Metadata.Namespace, p.Metadata.Name = ns, pod
	p.Status.Phase = "Pending"
	p.Status.Conditions = []kube.PodCondition{
		{Type: "PodScheduled", Status: "False", Reason: "Unschedulable", Message: msg},
	}
	return p
}

func blockedSvc(name, ns, workload string) Service {
	return Service{Name: name, Owned: []Component{{Kind: "Deployment", Namespace: ns, Name: workload}}}
}

func TestFillPendingResourcePressure(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("airflow", "airflow", "airflow-worker"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("airflow", "airflow-worker-866b459595-pgk7",
			"0/12 nodes are available: 1 Insufficient memory, 6 Insufficient cpu, 5 node(s) didn't match Pod's node affinity/selector."),
	}}, nil)

	b := snap.Services[0].Blocked
	if b == nil {
		t.Fatal("service should be blocked")
	}
	if got := b.Reason(); got != "no cpu, memory" {
		t.Errorf("Reason=%q, want %q", got, "no cpu, memory")
	}
	if !b.NoNodeMatched {
		t.Error("the message also says nothing matched, which should be recorded")
	}
	if snap.Summary.Blocked != 1 {
		t.Errorf("Summary.Blocked=%d, want 1", snap.Summary.Blocked)
	}
}

// A GPU service pinned to an empty nodegroup: no resource is named, every node is
// simply excluded. It is still blocked, and the reason has to say something useful.
func TestFillPendingNoMatchingNode(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("tts", "api", "inference"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("api", "inference-7d9f4c8b6-x2k4p",
			"0/12 nodes are available: 2 node(s) had untolerated taint(s), 10 node(s) didn't match Pod's node affinity/selector."),
	}}, nil)

	b := snap.Services[0].Blocked
	if b == nil || b.Reason() != "no matching node" {
		t.Fatalf("Reason=%v, want 'no matching node'", b)
	}
	if len(b.Resources) != 0 {
		t.Errorf("no resource is named in this message, got %v", b.Resources)
	}
}

// "api" and "api-canary" both prefix a daphne pod. Only the longer one
// owns it, otherwise a stuck pod greys out the wrong row.
func TestFillPendingLongestPrefixWins(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("short", "tts", "api"))
	snap.addService(blockedSvc("long", "tts", "api-canary"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("tts", "api-canary-6b8d7f9c5-mn3qz", "0/10 nodes are available: 10 Insufficient cpu."),
	}}, nil)

	// Look rows up by name: marking a row blocked re-orders the list.
	byName := map[string]*Service{}
	for i := range snap.Services {
		byName[snap.Services[i].Name] = &snap.Services[i]
	}
	if byName["short"].Blocked != nil {
		t.Error("the shorter-named service must not be blamed")
	}
	if byName["long"].Blocked == nil {
		t.Fatal("the owning service should be blocked")
	}
}

// Namespace has to match too: same workload name in two namespaces is common.
func TestFillPendingRespectsNamespace(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("a", "ns-a", "api"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ns-b", "api-1234-abcde", "0/3 nodes are available: 3 Insufficient cpu."),
	}}, nil)
	if snap.Services[0].Blocked != nil {
		t.Fatal("an event in another namespace must not block this service")
	}
}

func TestFillPendingCountsPodsAndDedupesResources(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("svc", "ml", "worker"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ml", "worker-1-a", "0/5 nodes are available: 5 Insufficient cpu."),
		pendingPod("ml", "worker-1-b", "0/5 nodes are available: 5 Insufficient cpu, 2 Insufficient nvidia.com/gpu."),
	}}, nil)
	b := snap.Services[0].Blocked
	if b.Pods != 2 {
		t.Errorf("Pods=%d, want 2", b.Pods)
	}
	if got := b.Reason(); got != "no cpu, nvidia.com/gpu" {
		t.Errorf("Reason=%q, want deduped and sorted", got)
	}
}

func TestFillPendingNoEventsIsNoop(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("svc", "ml", "worker"))
	FillPending(snap, nil, nil)
	FillPending(snap, &kube.PodList{}, nil)
	if snap.Services[0].Blocked != nil || snap.Summary.Blocked != 0 {
		t.Fatal("no events must mean no claims")
	}
}

// A blocked service must not sit below healthy rows. ArgoCD reported one as Healthy
// while several of its pods could not be scheduled, and it sorted far down the list.
func TestBlockedRowsSortAboveHealthyOnes(t *testing.T) {
	snap := &Snapshot{}
	for _, n := range []string{"aaa-ok", "bbb-ok", "zzz-blocked"} {
		svc := blockedSvc(n, "ml", n)
		svc.State = StateOK
		snap.addService(svc)
	}
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ml", "zzz-blocked-1-a", "0/5 nodes are available: 5 Insufficient cpu."),
	}}, nil)

	if snap.Services[0].Name != "zzz-blocked" {
		t.Fatalf("first row is %q, want the blocked one despite its name sorting last",
			snap.Services[0].Name)
	}
}

// A genuinely broken service still outranks a blocked one: DEGRADED means it is running
// and failing, which is worse than not having started.
func TestDegradedStillOutranksBlocked(t *testing.T) {
	snap := &Snapshot{}
	deg := blockedSvc("aaa-degraded", "ml", "aaa-degraded")
	deg.State = StateDegraded
	snap.addService(deg)
	ok := blockedSvc("zzz-blocked", "ml", "zzz-blocked")
	ok.State = StateOK
	snap.addService(ok)

	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ml", "zzz-blocked-1-a", "0/5 nodes are available: 5 Insufficient cpu."),
	}}, nil)

	if snap.Services[0].Name != "aaa-degraded" {
		t.Fatalf("first row is %q, want the degraded one", snap.Services[0].Name)
	}
}

// The two shortages need different people to fix them, so they are counted apart.
func TestBlockedKindSeparatesAcceleratorFromCPU(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("gpu-hungry", "ml", "gpu-hungry"))
	snap.addService(blockedSvc("cpu-hungry", "ml", "cpu-hungry"))
	snap.addService(blockedSvc("nowhere", "ml", "nowhere"))

	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ml", "gpu-hungry-1-a", "0/9 nodes are available: 9 Insufficient nvidia.com/gpu."),
		pendingPod("ml", "cpu-hungry-1-a", "0/9 nodes are available: 4 Insufficient cpu, 5 Insufficient memory."),
		pendingPod("ml", "nowhere-1-a", "0/9 nodes are available: 9 node(s) didn't match Pod's node affinity/selector."),
	}}, nil)

	kinds := map[string]string{}
	for _, s := range snap.Services {
		if s.Blocked != nil {
			kinds[s.Name] = s.Blocked.Kind()
		}
	}
	for name, want := range map[string]string{
		"gpu-hungry": "accelerator", "cpu-hungry": "cpu", "nowhere": "placement",
	} {
		if kinds[name] != want {
			t.Errorf("%s: Kind=%q, want %q", name, kinds[name], want)
		}
	}
	if snap.Summary.BlockedGPU != 1 || snap.Summary.BlockedCPU != 1 || snap.Summary.BlockedPlacement != 1 {
		t.Errorf("counters gpu/cpu/placement = %d/%d/%d, want 1/1/1",
			snap.Summary.BlockedGPU, snap.Summary.BlockedCPU, snap.Summary.BlockedPlacement)
	}
	if snap.Summary.Blocked != 3 {
		t.Errorf("Blocked=%d, want 3", snap.Summary.Blocked)
	}
}

// A message naming both counts as an accelerator shortage: that is the scarce thing.
func TestBlockedKindPrefersAccelerator(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("svc", "ml", "svc"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ml", "svc-1-a", "0/9 nodes are available: 4 Insufficient cpu, 5 Insufficient nvidia.com/gpu."),
	}}, nil)
	if got := snap.Services[0].Blocked.Kind(); got != "accelerator" {
		t.Fatalf("Kind=%q, want accelerator", got)
	}
}

// "No matching node" is true but not actionable. When the workload pins itself to one
// nodegroup, saying which one turns it into something a reader can act on.
func TestBlockedNamesTheEmptyNodegroup(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("lily", "inference-ns", "inference"))

	w := kube.Workload{Kind: "Deployment"}
	w.Metadata.Namespace, w.Metadata.Name = "inference-ns", "inference"
	w.Spec.Template.Spec.Affinity = &kube.Affinity{NodeAffinity: &kube.NodeAffinity{
		Required: &kube.NodeSelectorSpec{NodeSelectorTerms: []kube.NodeSelectorTerm{{
			MatchExpressions: []kube.NodeSelectorRequirement{
				{Key: "NodeGroupType", Operator: "In", Values: []string{"gpu-pool"}},
				{Key: "color", Operator: "In", Values: []string{"blue"}},
			},
		}}},
	}}

	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("inference-ns", "inference-7d9f4c8b6-x2k4p",
			"0/12 nodes are available: 2 node(s) had untolerated taint(s), 10 node(s) didn't match Pod's node affinity/selector."),
	}}, &kube.WorkloadList{Items: []kube.Workload{w}})

	b := snap.Services[0].Blocked
	if b == nil {
		t.Fatal("service should be blocked")
	}
	if got := b.Reason(); got != "no blue/gpu-pool node" {
		t.Fatalf("Reason=%q, want the nodegroup named", got)
	}
}

// Without the workload list it still says something true, just less useful.
func TestBlockedFallsBackWhenSelectorUnknown(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("svc", "ml", "svc"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ml", "svc-1-a", "0/9 nodes are available: 9 node(s) didn't match Pod's node affinity/selector."),
	}}, nil)
	if got := snap.Services[0].Blocked.Reason(); got != "no matching node" {
		t.Fatalf("Reason=%q, want the generic fallback", got)
	}
}

// A resource shortage keeps naming the resource; the selector is only for the case
// where the scheduler named nothing.
func TestBlockedSelectorDoesNotOverrideAResourceShortage(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("svc", "ml", "svc"))
	w := kube.Workload{Kind: "Deployment"}
	w.Metadata.Namespace, w.Metadata.Name = "ml", "svc"
	w.Spec.Template.Spec.NodeSelector = map[string]string{"pool": "big"}
	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		pendingPod("ml", "svc-1-a", "0/9 nodes are available: 9 Insufficient cpu, 2 node(s) didn't match Pod's node affinity/selector."),
	}}, &kube.WorkloadList{Items: []kube.Workload{w}})
	if got := snap.Services[0].Blocked.Reason(); got != "no cpu" {
		t.Fatalf("Reason=%q, want the resource shortage", got)
	}
}

// runningPod is a pod that was unschedulable earlier and has since been placed. Under
// the events approach its hour-old FailedScheduling event kept the service flagged: on
// a live cluster every pod of a service was Running while the page reported it short of
// cpu. Reading the pod's own condition cannot make that mistake.
func runningPod(ns, name string) kube.Pod {
	var p kube.Pod
	p.Metadata.Namespace, p.Metadata.Name = ns, name
	p.Status.Phase = "Running"
	p.Status.Conditions = []kube.PodCondition{{Type: "PodScheduled", Status: "True"}}
	return p
}

func TestScheduledPodIsNotReportedAsBlocked(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("api", "ml", "api"))

	FillPending(snap, &kube.PodList{Items: []kube.Pod{
		runningPod("ml", "api-b67747ccf-26c6r"),
	}}, nil)

	if snap.Services[0].Blocked != nil {
		t.Fatalf("a running pod must not be reported as blocked, got %v", snap.Services[0].Blocked.Reason())
	}
	if snap.Summary.Blocked != 0 {
		t.Errorf("Summary.Blocked=%d, want 0", snap.Summary.Blocked)
	}
}

// A pod can be Pending for reasons that are not the scheduler's: pulling an image, or
// waiting on an init container. Only an explicit Unschedulable verdict counts.
func TestPendingButSchedulableIsNotBlocked(t *testing.T) {
	var p kube.Pod
	p.Metadata.Namespace, p.Metadata.Name = "ml", "api-1-a"
	p.Status.Phase = "Pending"
	p.Status.Conditions = []kube.PodCondition{{Type: "PodScheduled", Status: "True"}}

	snap := &Snapshot{}
	snap.addService(blockedSvc("api", "ml", "api"))
	FillPending(snap, &kube.PodList{Items: []kube.Pod{p}}, nil)

	if snap.Services[0].Blocked != nil {
		t.Fatal("pending for another reason is not a scheduling failure")
	}
}

// ownerReferences name the workload exactly, which beats guessing from the pod name.
func TestOwnerReferenceBeatsNamePrefix(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("api", "ml", "api"))
	snap.addService(blockedSvc("canary", "ml", "api-canary"))

	p := pendingPod("ml", "api-canary-6b8d7f9c5-mn3qz", "0/12 nodes are available: 12 Insufficient cpu.")
	p.Metadata.OwnerReferences = []kube.OwnerReference{{Kind: "ReplicaSet", Name: "api-canary-6b8d7f9c5"}}
	FillPending(snap, &kube.PodList{Items: []kube.Pod{p}}, nil)

	byName := map[string]*Service{}
	for i := range snap.Services {
		byName[snap.Services[i].Name] = &snap.Services[i]
	}
	if byName["api"].Blocked != nil {
		t.Error("the wrong service was blamed")
	}
	if byName["canary"].Blocked == nil {
		t.Fatal("the owning service should be blocked")
	}
}

package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func schedEvent(ns, pod, msg string) kube.Event {
	var e kube.Event
	e.Metadata.Namespace = ns
	e.InvolvedObject = kube.InvolvedObject{Kind: "Pod", Name: pod, Namespace: ns}
	e.Reason, e.Message = "FailedScheduling", msg
	return e
}

func blockedSvc(name, ns, workload string) Service {
	return Service{Name: name, Owned: []Component{{Kind: "Deployment", Namespace: ns, Name: workload}}}
}

func TestFillPendingResourcePressure(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("airflow", "airflow", "airflow-worker"))
	FillPending(snap, &kube.EventList{Items: []kube.Event{
		schedEvent("airflow", "airflow-worker-866b459595-pgk7",
			"0/84 nodes are available: 1 Insufficient memory, 6 Insufficient cpu, 75 node(s) didn't match Pod's node affinity/selector."),
	}})

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
	snap.addService(blockedSvc("tts", "tts-engine", "lily-tts-pytriton"))
	FillPending(snap, &kube.EventList{Items: []kube.Event{
		schedEvent("tts-engine", "lily-tts-pytriton-7669bf497f-nr6t6",
			"0/86 nodes are available: 4 node(s) had untolerated taint(s), 82 node(s) didn't match Pod's node affinity/selector."),
	}})

	b := snap.Services[0].Blocked
	if b == nil || b.Reason() != "no matching node" {
		t.Fatalf("Reason=%v, want 'no matching node'", b)
	}
	if len(b.Resources) != 0 {
		t.Errorf("no resource is named in this message, got %v", b.Resources)
	}
}

// "tts-engine" and "tts-engine-daphne" both prefix a daphne pod. Only the longer one
// owns it, otherwise a stuck pod greys out the wrong row.
func TestFillPendingLongestPrefixWins(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("short", "tts", "tts-engine"))
	snap.addService(blockedSvc("long", "tts", "tts-engine-daphne"))
	FillPending(snap, &kube.EventList{Items: []kube.Event{
		schedEvent("tts", "tts-engine-daphne-6db5fc7c74-2cw52", "0/10 nodes are available: 10 Insufficient cpu."),
	}})

	if snap.Services[0].Blocked != nil {
		t.Error("the shorter-named service must not be blamed")
	}
	if snap.Services[1].Blocked == nil {
		t.Fatal("the owning service should be blocked")
	}
}

// Namespace has to match too: same workload name in two namespaces is common.
func TestFillPendingRespectsNamespace(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("a", "ns-a", "api"))
	FillPending(snap, &kube.EventList{Items: []kube.Event{
		schedEvent("ns-b", "api-1234-abcde", "0/3 nodes are available: 3 Insufficient cpu."),
	}})
	if snap.Services[0].Blocked != nil {
		t.Fatal("an event in another namespace must not block this service")
	}
}

func TestFillPendingCountsPodsAndDedupesResources(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(blockedSvc("svc", "ml", "worker"))
	FillPending(snap, &kube.EventList{Items: []kube.Event{
		schedEvent("ml", "worker-1-a", "0/5 nodes are available: 5 Insufficient cpu."),
		schedEvent("ml", "worker-1-b", "0/5 nodes are available: 5 Insufficient cpu, 2 Insufficient nvidia.com/gpu."),
	}})
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
	FillPending(snap, nil)
	FillPending(snap, &kube.EventList{})
	if snap.Services[0].Blocked != nil || snap.Summary.Blocked != 0 {
		t.Fatal("no events must mean no claims")
	}
}

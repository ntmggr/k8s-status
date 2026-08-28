package status

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func zoneNode(name, zone, ip string) kube.Node {
	var n kube.Node
	n.Metadata.Name = name
	if zone != "" {
		n.Metadata.Labels = map[string]string{kube.LabelZone: zone}
	}
	n.Status.Addresses = []kube.NodeAddress{{Type: "InternalIP", Address: ip}}
	return n
}

func zoneRunningPod(ns, name, owner, hostIP string) kube.Pod {
	var p kube.Pod
	p.Metadata.Namespace, p.Metadata.Name = ns, name
	p.Metadata.OwnerReferences = []kube.OwnerReference{{Kind: "Deployment", Name: owner}}
	p.Status.Phase = "Running"
	p.Status.HostIP = hostIP
	return p
}

func zoneSvc(name, ns, workload string) Service {
	return Service{Name: name, Owned: []Component{{Kind: "Deployment", Namespace: ns, Name: workload}}}
}

func TestFillZonesNilInputsAreNoop(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("svc", "ml", "worker"))
	FillZones(nil, nil, nil)
	FillZones(snap, nil, &kube.NodeList{})
	FillZones(snap, &kube.PodList{}, nil)
	if snap.Services[0].Zones.Known() {
		t.Fatal("no pods or nodes should mean no zone answer")
	}
}

func TestFillZonesThreeZoneSpread(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{
		zoneNode("n1", "eu-west-1a", "10.0.1.1"),
		zoneNode("n2", "eu-west-1b", "10.0.1.2"),
		zoneNode("n3", "eu-west-1c", "10.0.1.3"),
	}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("ml", "api-1", "api", "10.0.1.1"),
		zoneRunningPod("ml", "api-2", "api", "10.0.1.2"),
		zoneRunningPod("ml", "api-3", "api", "10.0.1.3"),
	}}
	FillZones(snap, pods, nodes)

	z := snap.Services[0].Zones
	if z.Count() != 3 || z.Pods != 3 || z.Unplaced != 0 {
		t.Fatalf("Zones = %+v, want 3 zones, 3 pods, 0 unplaced", z)
	}
	want := []string{"eu-west-1a", "eu-west-1b", "eu-west-1c"}
	for i, w := range want {
		if i >= len(z.Zones) || z.Zones[i] != w {
			t.Errorf("Zones = %v, want %v", z.Zones, want)
			break
		}
	}
	if snap.Summary.MultiZone != 1 || snap.Summary.SingleZone != 0 || snap.Summary.Zoned != 1 {
		t.Errorf("Summary = zoned=%d single=%d multi=%d, want 1/0/1",
			snap.Summary.Zoned, snap.Summary.SingleZone, snap.Summary.MultiZone)
	}
}

func TestFillZonesAllOnOneNodeIsSingleZone(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("ml", "api-1", "api", "10.0.1.1"),
		zoneRunningPod("ml", "api-2", "api", "10.0.1.1"),
	}}
	FillZones(snap, pods, nodes)

	z := snap.Services[0].Zones
	if z.Count() != 1 || z.Pods != 2 {
		t.Fatalf("Zones = %+v, want 1 zone, 2 pods", z)
	}
	if snap.Summary.SingleZone != 1 || snap.Summary.MultiZone != 0 {
		t.Errorf("single=%d multi=%d, want 1/0", snap.Summary.SingleZone, snap.Summary.MultiZone)
	}
}

func TestFillZonesUnmatchedHostIPCountsAsUnplaced(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("ml", "api-1", "api", "10.0.9.99"), // no node has this IP
	}}
	FillZones(snap, pods, nodes)

	z := snap.Services[0].Zones
	if z.Unplaced != 1 {
		t.Errorf("Unplaced=%d, want 1", z.Unplaced)
	}
	if z.Known() {
		t.Error("a pod that could not be placed must not count as a known zone answer")
	}
	if snap.Summary.Zoned != 0 {
		t.Errorf("Zoned=%d, want 0", snap.Summary.Zoned)
	}
}

func TestFillZonesUnlabelledNodeCountsAsUnplaced(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "", "10.0.1.1")}} // no zone label
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("ml", "api-1", "api", "10.0.1.1"),
	}}
	FillZones(snap, pods, nodes)

	z := snap.Services[0].Zones
	if z.Unplaced != 1 || z.Count() != 0 {
		t.Fatalf("Zones = %+v, want 0 zones and 1 unplaced", z)
	}
}

func TestFillZonesIgnoresPendingPods(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pending := zoneRunningPod("ml", "api-1", "api", "10.0.1.1")
	pending.Status.Phase = "Pending"
	pods := &kube.PodList{Items: []kube.Pod{pending}}
	FillZones(snap, pods, nodes)

	z := snap.Services[0].Zones
	if z.Pods != 0 || z.Known() {
		t.Errorf("Zones = %+v, want a Pending pod to be silently ignored", z)
	}
}

func TestFillZonesResetsOnRepeatedCalls(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{zoneRunningPod("ml", "api-1", "api", "10.0.1.1")}}

	for i := 0; i < 3; i++ {
		FillZones(snap, pods, nodes)
	}
	z := snap.Services[0].Zones
	if z.Pods != 1 {
		t.Errorf("Pods=%d after 3 calls, want 1 (must not accumulate)", z.Pods)
	}
	if snap.Summary.SingleZone != 1 {
		t.Errorf("SingleZone=%d after 3 calls, want 1", snap.Summary.SingleZone)
	}
	n := snap.Services[0].Nodes
	if n.Pods != 1 || n.Count() != 1 {
		t.Errorf("Nodes=%+v after 3 calls, want 1 pod, 1 node (must not accumulate)", n)
	}
	if snap.Summary.SingleNode != 1 || snap.Summary.MultiNode != 0 {
		t.Errorf("SingleNode/MultiNode = %d/%d after 3 calls, want 1/0",
			snap.Summary.SingleNode, snap.Summary.MultiNode)
	}
}

// TestFillZonesTracksNodesIndependentlyOfZones proves zone-count and node-count are
// independent: two pods on two different nodes that both sit in the same zone must
// count as single-zone (1) but multi-node (2).
func TestFillZonesTracksNodesIndependentlyOfZones(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{
		zoneNode("n1", "eu-west-1a", "10.0.1.1"),
		zoneNode("n2", "eu-west-1a", "10.0.1.2"),
	}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("ml", "api-1", "api", "10.0.1.1"),
		zoneRunningPod("ml", "api-2", "api", "10.0.1.2"),
	}}
	FillZones(snap, pods, nodes)

	z := snap.Services[0].Zones
	if z.Count() != 1 {
		t.Errorf("Zones.Count()=%d, want 1 (same zone)", z.Count())
	}
	n := snap.Services[0].Nodes
	if n.Count() != 2 {
		t.Errorf("Nodes.Count()=%d, want 2 (different nodes)", n.Count())
	}
	if snap.Summary.SingleZone != 1 || snap.Summary.MultiZone != 0 {
		t.Errorf("SingleZone/MultiZone = %d/%d, want 1/0", snap.Summary.SingleZone, snap.Summary.MultiZone)
	}
	if snap.Summary.SingleNode != 0 || snap.Summary.MultiNode != 1 {
		t.Errorf("SingleNode/MultiNode = %d/%d, want 0/1", snap.Summary.SingleNode, snap.Summary.MultiNode)
	}
}

func TestHA(t *testing.T) {
	cases := []struct {
		name                 string
		zoned, single, multi int
		wantKnown            bool
		wantPercent          int
	}{
		{"all compliant", 4, 0, 4, true, 100},
		{"mixed", 4, 1, 3, true, 75},
		{"zero eligible", 0, 0, 0, false, 0},
		{"all at risk", 3, 3, 0, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &Snapshot{Summary: Summary{Zoned: tc.zoned, SingleZone: tc.single, MultiZone: tc.multi}}
			h := snap.HA()
			if h.Known != tc.wantKnown {
				t.Errorf("Known=%v, want %v", h.Known, tc.wantKnown)
			}
			if h.Percent != tc.wantPercent {
				t.Errorf("Percent=%d, want %d", h.Percent, tc.wantPercent)
			}
			if h.Eligible != tc.zoned || h.Compliant != tc.multi || h.AtRisk != tc.single {
				t.Errorf("HA=%+v, want eligible=%d compliant=%d atRisk=%d", h, tc.zoned, tc.multi, tc.single)
			}
		})
	}
}

func TestNodeHA(t *testing.T) {
	cases := []struct {
		name          string
		single, multi int
		wantKnown     bool
		wantPercent   int
	}{
		{"all compliant", 0, 4, true, 100},
		{"mixed", 1, 3, true, 75},
		{"zero eligible", 0, 0, false, 0},
		{"all at risk", 3, 0, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &Snapshot{Summary: Summary{SingleNode: tc.single, MultiNode: tc.multi}}
			n := snap.NodeHA()
			if n.Known != tc.wantKnown {
				t.Errorf("Known=%v, want %v", n.Known, tc.wantKnown)
			}
			if n.Percent != tc.wantPercent {
				t.Errorf("Percent=%d, want %d", n.Percent, tc.wantPercent)
			}
			if n.Eligible != tc.single+tc.multi || n.Compliant != tc.multi || n.AtRisk != tc.single {
				t.Errorf("NodeHA=%+v, want eligible=%d compliant=%d atRisk=%d",
					n, tc.single+tc.multi, tc.multi, tc.single)
			}
		})
	}
}

func TestZoneReadErrorClassifiesDenied(t *testing.T) {
	err := &kube.StatusError{Code: http.StatusForbidden, Body: "forbidden"}
	ze := zoneReadError(err)
	if !ze.Denied || ze.TooLarge || ze.Missing {
		t.Errorf("ZoneError = %+v, want Denied only", ze)
	}
}

func TestZoneReadErrorClassifiesTooLarge(t *testing.T) {
	ze := zoneReadError(kube.ErrPodListTooLarge)
	if !ze.TooLarge || ze.Denied {
		t.Errorf("ZoneError = %+v, want TooLarge only", ze)
	}
}

func TestZoneReadErrorWrapsErrorsIs(t *testing.T) {
	wrapped := errors.Join(kube.ErrPodListTooLarge)
	ze := zoneReadError(wrapped)
	if !ze.TooLarge {
		t.Errorf("ZoneError = %+v, want TooLarge for a wrapped sentinel", ze)
	}
}

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
	FillZones(nil, nil, nil, nil, "", nil)
	FillZones(snap, nil, &kube.NodeList{}, nil, "", nil)
	FillZones(snap, &kube.PodList{}, nil, nil, "", nil)
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
	FillZones(snap, pods, nodes, nil, "", nil)

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
	FillZones(snap, pods, nodes, nil, "", nil)

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
	FillZones(snap, pods, nodes, nil, "", nil)

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
	FillZones(snap, pods, nodes, nil, "", nil)

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
	FillZones(snap, pods, nodes, nil, "", nil)

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
		FillZones(snap, pods, nodes, nil, "", nil)
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
	FillZones(snap, pods, nodes, nil, "", nil)

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

func unmanagedWorkload(ns, kind, name string) kube.Workload {
	var w kube.Workload
	w.Kind = kind
	w.Metadata.Namespace, w.Metadata.Name = ns, name
	return w
}

// TestFillZonesFoldsUnmanagedWorkloadsIntoHA is the case the "include services that
// are not gitops in the HA" request is about: an unmanaged Deployment's pods have no
// Service row of their own, but must still move the same Summary totals the HA/NodeHA
// gauges read, and must not be double-counted against the GitOps service also present.
func TestFillZonesFoldsUnmanagedWorkloadsIntoHA(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api")) // GitOps service, spread across 2 zones
	nodes := &kube.NodeList{Items: []kube.Node{
		zoneNode("n1", "eu-west-1a", "10.0.1.1"),
		zoneNode("n2", "eu-west-1b", "10.0.1.2"),
	}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("ml", "api-1", "api", "10.0.1.1"),
		zoneRunningPod("ml", "api-2", "api", "10.0.1.2"),
		zoneRunningPod("batch", "worker-1", "worker", "10.0.1.1"), // unmanaged, single zone
	}}
	workloads := &kube.WorkloadList{Items: []kube.Workload{
		unmanagedWorkload("batch", "Deployment", "worker"),
	}}
	FillZones(snap, pods, nodes, workloads, "", nil)

	// The GitOps service still gets its own per-item answer, unaffected by the fold.
	if z := snap.Services[0].Zones; z.Count() != 2 {
		t.Errorf("Services[0].Zones.Count()=%d, want 2", z.Count())
	}
	// Summary folds both: the service (multi-zone) and the unmanaged workload (single
	// zone, since both its host IPs share one zone... here it's one pod on one node).
	if snap.Summary.MultiZone != 1 {
		t.Errorf("MultiZone=%d, want 1 (the GitOps service)", snap.Summary.MultiZone)
	}
	if snap.Summary.SingleZone != 1 {
		t.Errorf("SingleZone=%d, want 1 (the folded unmanaged workload)", snap.Summary.SingleZone)
	}
	if snap.Summary.Zoned != 2 {
		t.Errorf("Zoned=%d, want 2 total", snap.Summary.Zoned)
	}
	if snap.Summary.SingleNode != 1 || snap.Summary.MultiNode != 1 {
		t.Errorf("SingleNode/MultiNode = %d/%d, want 1/1", snap.Summary.SingleNode, snap.Summary.MultiNode)
	}
}

// TestFillZonesUnmanagedFoldIsIdempotent guards the same accumulation-across-repeated-
// calls bug the GitOps path already tests for (TestFillZonesResetsOnRepeatedCalls), but
// for the unmanaged fold's own local maps.
func TestFillZonesUnmanagedFoldIsIdempotent(t *testing.T) {
	snap := &Snapshot{}
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("batch", "worker-1", "worker", "10.0.1.1"),
	}}
	workloads := &kube.WorkloadList{Items: []kube.Workload{
		unmanagedWorkload("batch", "Deployment", "worker"),
	}}

	for i := 0; i < 3; i++ {
		FillZones(snap, pods, nodes, workloads, "", nil)
	}
	if snap.Summary.Zoned != 1 || snap.Summary.SingleZone != 1 {
		t.Errorf("Zoned/SingleZone = %d/%d after 3 calls, want 1/1 (no accumulation)",
			snap.Summary.Zoned, snap.Summary.SingleZone)
	}
}

// TestFillZonesExcludesMeshNamespaceFromMeshCoverageOnly is the istiod case found on a
// real cluster: the mesh control plane is unmanaged (no ArgoCD tracking) and correctly
// has no sidecar of its own -- asking whether it injected itself is a nonsense
// question, not a real gap, and showed up as a false "not in mesh" alarm. Zone/node
// spread must still be tracked normally: whether the control plane itself survives
// losing a zone is a real, separate question.
func TestFillZonesExcludesMeshNamespaceFromMeshCoverageOnly(t *testing.T) {
	snap := &Snapshot{}
	nodes := &kube.NodeList{Items: []kube.Node{
		zoneNode("n1", "eu-west-1a", "10.0.1.1"),
		zoneNode("n2", "eu-west-1b", "10.0.1.2"),
	}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("istio-system", "istiod-1", "istiod", "10.0.1.1"),
		zoneRunningPod("istio-system", "istiod-2", "istiod", "10.0.1.2"),
	}}
	workloads := &kube.WorkloadList{Items: []kube.Workload{
		unmanagedWorkload("istio-system", "Deployment", "istiod"),
	}}
	FillZones(snap, pods, nodes, workloads, "istio-system", nil)

	if snap.Summary.MeshEligible != 0 || snap.Summary.MeshInjected != 0 {
		t.Errorf("MeshEligible/MeshInjected = %d/%d, want 0/0 (mesh namespace excluded)",
			snap.Summary.MeshEligible, snap.Summary.MeshInjected)
	}
	// Zone spread is a different question and must not be affected by the exclusion.
	if snap.Summary.Zoned != 1 || snap.Summary.MultiZone != 1 {
		t.Errorf("Zoned/MultiZone = %d/%d, want 1/1 (spread still tracked normally)",
			snap.Summary.Zoned, snap.Summary.MultiZone)
	}
}

// helmWorkload builds an unmanaged workload that belongs to a Helm release, the same
// shape collapseReleases groups by.
func helmWorkload(ns, kind, name, release string) kube.Workload {
	w := unmanagedWorkload(ns, kind, name)
	w.Metadata.Labels = map[string]string{
		labelInstance:  release,
		labelManagedBy: "Helm",
	}
	return w
}

// TestFillZonesGroupsUnmanagedByReleaseNotByRawItem is the case the table's own
// collapsing exposed: two raw Deployments belonging to the same Helm release are one
// row in the not-in-gitops table, so they must also be one thing counted toward
// Summary, not two -- otherwise a release with several single-zone members inflates
// "at risk" past what the table actually shows.
func TestFillZonesGroupsUnmanagedByReleaseNotByRawItem(t *testing.T) {
	snap := &Snapshot{}
	snap.Unmanaged = &Unmanaged{}
	nodes := &kube.NodeList{Items: []kube.Node{
		zoneNode("n1", "eu-west-1a", "10.0.1.1"),
	}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningPod("gw", "gw-a-1", "gw-a", "10.0.1.1"),
		zoneRunningPod("gw", "gw-b-1", "gw-b", "10.0.1.1"),
	}}
	workloads := &kube.WorkloadList{Items: []kube.Workload{
		helmWorkload("gw", "Deployment", "gw-a", "gw"),
		helmWorkload("gw", "Deployment", "gw-b", "gw"),
	}}
	// Mirror BuildUnmanaged's own collapsing so Unmanaged.Items has the one row
	// FillZones must find by key, the same order attachUnmanaged/attachZones run in.
	snap.Unmanaged.Items = collapseReleases([]Workload{
		{Namespace: "gw", Kind: "Deployment", Name: "gw-a", Release: "gw/gw", Members: 1},
		{Namespace: "gw", Kind: "Deployment", Name: "gw-b", Release: "gw/gw", Members: 1},
	})

	FillZones(snap, pods, nodes, workloads, "", nil)

	if got := len(snap.Unmanaged.Items); got != 1 {
		t.Fatalf("collapsed rows=%d, want 1 (one release row)", got)
	}
	// Both members' pods sit in the same zone/node, so the one row is single-zone --
	// counted once, not twice, even though it stands for two raw Deployments.
	if snap.Summary.SingleZone != 1 || snap.Summary.MultiZone != 0 {
		t.Errorf("SingleZone/MultiZone = %d/%d, want 1/0 (one release, not two members)",
			snap.Summary.SingleZone, snap.Summary.MultiZone)
	}
	if snap.Summary.SingleNode != 1 || snap.Summary.MultiNode != 0 {
		t.Errorf("SingleNode/MultiNode = %d/%d, want 1/0", snap.Summary.SingleNode, snap.Summary.MultiNode)
	}
	row := snap.Unmanaged.Items[0]
	if !row.Zones.Known() || row.Zones.Count() != 1 {
		t.Errorf("row.Zones = %+v, want a known single-zone answer", row.Zones)
	}
	// Both members' pods count toward the row's Pods total, not just one member's.
	if row.Zones.Pods != 2 {
		t.Errorf("row.Zones.Pods=%d, want 2 (both release members' pods)", row.Zones.Pods)
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

// zoneRunningInjectedPod is zoneRunningPod plus an istio-proxy container status, for
// mesh coverage tests.
func zoneRunningInjectedPod(ns, name, owner, hostIP string) kube.Pod {
	p := zoneRunningPod(ns, name, owner, hostIP)
	p.Status.ContainerStatuses = []kube.ContainerStatus{{Name: "istio-proxy"}}
	return p
}

func TestFillZonesMeshCoveragePartiallyInjected(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningInjectedPod("ml", "api-1", "api", "10.0.1.1"),
		zoneRunningPod("ml", "api-2", "api", "10.0.1.1"),
	}}
	FillZones(snap, pods, nodes, nil, "", nil)

	m := snap.Services[0].Mesh
	if !m.Known() || m.Full() {
		t.Fatalf("Mesh = %+v, want known but not full (only one of two pods injected)", m)
	}
	if m.Pods != 2 || m.Injected != 1 {
		t.Errorf("Mesh = %+v, want 2 pods, 1 injected", m)
	}
	if snap.Summary.MeshEligible != 1 || snap.Summary.MeshInjected != 0 {
		t.Errorf("MeshEligible/MeshInjected = %d/%d, want 1/0", snap.Summary.MeshEligible, snap.Summary.MeshInjected)
	}
}

func TestFillZonesMeshCoverageFullyInjected(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningInjectedPod("ml", "api-1", "api", "10.0.1.1"),
		zoneRunningInjectedPod("ml", "api-2", "api", "10.0.1.1"),
	}}
	FillZones(snap, pods, nodes, nil, "", nil)

	m := snap.Services[0].Mesh
	if !m.Full() {
		t.Fatalf("Mesh = %+v, want Full (every pod injected)", m)
	}
	if snap.Summary.MeshEligible != 1 || snap.Summary.MeshInjected != 1 {
		t.Errorf("MeshEligible/MeshInjected = %d/%d, want 1/1", snap.Summary.MeshEligible, snap.Summary.MeshInjected)
	}
}

func TestFillZonesMeshCoverageNoPodsIsUnknown(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	FillZones(snap, &kube.PodList{}, &kube.NodeList{}, nil, "", nil)

	if snap.Services[0].Mesh.Known() {
		t.Fatal("no pods should mean no mesh answer")
	}
	if snap.Summary.MeshEligible != 0 || snap.Summary.MeshInjected != 0 {
		t.Errorf("MeshEligible/MeshInjected = %d/%d, want 0/0", snap.Summary.MeshEligible, snap.Summary.MeshInjected)
	}
}

func TestFillZonesMeshCoverageResetsOnRepeatedCalls(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("api", "ml", "api"))
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningInjectedPod("ml", "api-1", "api", "10.0.1.1"),
	}}
	for i := 0; i < 3; i++ {
		FillZones(snap, pods, nodes, nil, "", nil)
	}
	if m := snap.Services[0].Mesh; m.Pods != 1 || m.Injected != 1 {
		t.Errorf("Mesh accumulated across repeated calls: %+v", m)
	}
	if snap.Summary.MeshEligible != 1 || snap.Summary.MeshInjected != 1 {
		t.Errorf("MeshEligible/MeshInjected accumulated: %d/%d, want 1/1", snap.Summary.MeshEligible, snap.Summary.MeshInjected)
	}
}

// TestFillZonesFoldsUnmanagedMeshCoverage mirrors TestFillZonesFoldsUnmanagedWorkloadsIntoHA
// for mesh coverage, but unlike zones/nodes, mesh coverage does NOT fold into the
// Istio gauges' own Summary.MeshEligible/MeshInjected -- those describe GitOps-tracked
// services specifically. An unmanaged workload's pods instead move the separate
// Summary.UnmanagedMeshEligible/UnmanagedMeshInjected counters, and the not-in-gitops
// row still gets its own Mesh answer for its per-row badge.
func TestFillZonesFoldsUnmanagedMeshCoverage(t *testing.T) {
	snap := &Snapshot{}
	snap.Unmanaged = &Unmanaged{}
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.1.1")}}
	pods := &kube.PodList{Items: []kube.Pod{
		zoneRunningInjectedPod("batch", "worker-1", "worker", "10.0.1.1"),
	}}
	workloads := &kube.WorkloadList{Items: []kube.Workload{
		unmanagedWorkload("batch", "Deployment", "worker"),
	}}
	snap.Unmanaged.Items = collapseReleases([]Workload{
		{Namespace: "batch", Kind: "Deployment", Name: "worker", Members: 1},
	})

	FillZones(snap, pods, nodes, workloads, "", nil)

	if snap.Summary.MeshEligible != 0 || snap.Summary.MeshInjected != 0 {
		t.Errorf("MeshEligible/MeshInjected = %d/%d, want 0/0 (not-in-gitops must not move the Istio gauges)", snap.Summary.MeshEligible, snap.Summary.MeshInjected)
	}
	if snap.Summary.UnmanagedMeshEligible != 1 || snap.Summary.UnmanagedMeshInjected != 1 {
		t.Errorf("UnmanagedMeshEligible/UnmanagedMeshInjected = %d/%d, want 1/1", snap.Summary.UnmanagedMeshEligible, snap.Summary.UnmanagedMeshInjected)
	}
	row := snap.Unmanaged.Items[0]
	if !row.Mesh.Full() {
		t.Errorf("row.Mesh = %+v, want Full", row.Mesh)
	}
}

func TestMeshHA(t *testing.T) {
	cases := []struct {
		name               string
		eligible, injected int
		installed          bool
		wantKnown          bool
		wantPercent        int
	}{
		{"all injected", 4, 4, true, true, 100},
		{"mixed", 4, 1, true, true, 25},
		{"zero eligible", 0, 0, true, false, 0},
		{"none injected", 3, 0, true, true, 0},
		// Istio not installed: Eligible is still nonzero (services still have running
		// pods), but there is no mesh to be "in" at all, so Known must be false rather
		// than reporting every service as a 0%/all-at-risk gap.
		{"istio not installed", 3, 0, false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &Snapshot{
				Summary: Summary{MeshEligible: tc.eligible, MeshInjected: tc.injected},
				Mesh:    &MeshSection{Installed: tc.installed},
			}
			m := snap.MeshHA()
			if m.Known != tc.wantKnown {
				t.Errorf("Known=%v, want %v", m.Known, tc.wantKnown)
			}
			if m.Percent != tc.wantPercent {
				t.Errorf("Percent=%d, want %d", m.Percent, tc.wantPercent)
			}
			if m.Eligible != tc.eligible || m.Injected != tc.injected {
				t.Errorf("MeshHA=%+v, want eligible=%d injected=%d", m, tc.eligible, tc.injected)
			}
			if want := tc.eligible - tc.injected; m.NotInMesh() != want {
				t.Errorf("NotInMesh()=%d, want %d", m.NotInMesh(), want)
			}
		})
	}
}

// TestMeshHANilMeshIsUnknown covers MESH_MTLS being off entirely (Snapshot.Mesh is
// never set), which must behave the same as "not installed", not panic or assume
// installed.
func TestMeshHANilMeshIsUnknown(t *testing.T) {
	snap := &Snapshot{Summary: Summary{MeshEligible: 3, MeshInjected: 0}}
	m := snap.MeshHA()
	if m.Known {
		t.Errorf("Known=true with a nil Mesh, want false")
	}
}

// TestMeshPolicyHA mirrors TestMeshHA for the per-service policy question: what
// share of policy-eligible services are NOT permissive. Unlike MeshHA there is no
// "Istio not installed" case to gate on -- MeshPolicyEligible is already zero
// whenever mesh mTLS or AZ_SPREAD is off, so zero-eligible alone covers it.
func TestMeshPolicyHA(t *testing.T) {
	cases := []struct {
		name                 string
		eligible, permissive int
		wantKnown            bool
		wantPercent          int
	}{
		{"none permissive", 4, 0, true, 100},
		{"mixed", 4, 1, true, 75},
		{"zero eligible", 0, 0, false, 0},
		{"all permissive", 3, 3, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &Snapshot{
				Summary: Summary{MeshPolicyEligible: tc.eligible, MeshPolicyPermissive: tc.permissive},
			}
			m := snap.MeshPolicyHA()
			if m.Known != tc.wantKnown {
				t.Errorf("Known=%v, want %v", m.Known, tc.wantKnown)
			}
			if m.Percent != tc.wantPercent {
				t.Errorf("Percent=%d, want %d", m.Percent, tc.wantPercent)
			}
			if m.Eligible != tc.eligible || m.Permissive != tc.permissive {
				t.Errorf("MeshPolicyHA=%+v, want eligible=%d permissive=%d", m, tc.eligible, tc.permissive)
			}
			if want := tc.eligible - tc.permissive; m.Compliant() != want {
				t.Errorf("Compliant()=%d, want %d", m.Compliant(), want)
			}
		})
	}
}

// TestFillZonesResolvesPerServicePolicyFromRunningPod is the wiring test for Policy:
// FillZones must read the namespace and labels off one of a service's own running
// pods and hand them to ResolveServicePolicy, not merely have the function available.
func TestFillZonesResolvesPerServicePolicyFromRunningPod(t *testing.T) {
	snap := &Snapshot{Mesh: &MeshSection{Installed: true, Effective: MeshStrict}}
	snap.addService(zoneSvc("billing", "payments", "billing"))

	pod := zoneRunningPod("payments", "billing-abc", "billing", "10.0.0.1")
	pod.Metadata.Labels = map[string]string{"app": "billing"}
	pods := &kube.PodList{Items: []kube.Pod{pod}}
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.0.1")}}
	peerAuths := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("payments", map[string]string{"app": "billing"}, "DISABLE"),
	}}

	FillZones(snap, pods, nodes, nil, "istio-system", peerAuths)

	got := snap.Services[0].Policy
	if !got.Known() || got.Effective != MeshDisabled || got.Scope != ScopeWorkload {
		t.Errorf("Policy = %+v, want {disabled workload known}", got)
	}
	if snap.Summary.MeshPolicyEligible != 1 {
		t.Errorf("MeshPolicyEligible = %d, want 1", snap.Summary.MeshPolicyEligible)
	}
	if snap.Summary.MeshPolicyPermissive != 0 {
		t.Errorf("MeshPolicyPermissive = %d, want 0", snap.Summary.MeshPolicyPermissive)
	}
}

// TestFillZonesPolicyUnknownWithoutMesh guards the common case, MESH_MTLS off, so
// Policy stays the zero value rather than resolving against a nil Mesh.
func TestFillZonesPolicyUnknownWithoutMesh(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(zoneSvc("billing", "payments", "billing"))
	pod := zoneRunningPod("payments", "billing-abc", "billing", "10.0.0.1")
	pods := &kube.PodList{Items: []kube.Pod{pod}}
	nodes := &kube.NodeList{Items: []kube.Node{zoneNode("n1", "eu-west-1a", "10.0.0.1")}}

	FillZones(snap, pods, nodes, nil, "", nil)

	if snap.Services[0].Policy.Known() {
		t.Errorf("Policy = %+v, want unknown with MESH_MTLS off", snap.Services[0].Policy)
	}
}

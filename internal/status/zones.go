package status

import (
	"errors"
	"math"
	"net/http"
	"sort"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// ZoneSpread describes how many of a service's running pods sit in each availability
// zone, worked out by joining a pod's status.hostIP to the node it landed on.
type ZoneSpread struct {
	// Zones is every distinct zone holding at least one running pod for this service,
	// sorted for a stable rendering.
	Zones []string
	// Pods is how many running pods were matched to this service, placed or not.
	Pods int
	// Unplaced counts running pods whose hostIP matched no known node, or whose node
	// carries no zone label. Neither says the pod is unhealthy, only that this section
	// cannot place it.
	Unplaced int
}

// Count is how many distinct zones this service's running pods actually sit in.
func (z ZoneSpread) Count() int { return len(z.Zones) }

// Known reports whether this service has a usable zone answer at all: at least one
// running pod, and at least one of them placed into a zone.
func (z ZoneSpread) Known() bool { return z.Pods > 0 && len(z.Zones) > 0 }

// NodeSpread describes how many of a service's running pods sit on each node,
// worked out the same way as ZoneSpread but joining a pod's status.hostIP to the
// node's name instead of its zone.
type NodeSpread struct {
	// Nodes is every distinct node name holding at least one running pod for this
	// service, sorted for a stable rendering.
	Nodes []string
	// Pods is how many running pods were matched to this service, placed or not.
	Pods int
}

// Count is how many distinct nodes this service's running pods actually sit on.
func (n NodeSpread) Count() int { return len(n.Nodes) }

// Known reports whether this service has a usable node answer at all: at least one
// running pod, and at least one of them placed onto a node.
func (n NodeSpread) Known() bool { return n.Pods > 0 && len(n.Nodes) > 0 }

// HA is a single at-a-glance number: what share of the services with a known zone
// answer actually spread across more than one zone. Derived entirely from Summary,
// mirroring Health's shape.
type HA struct {
	// Percent is Compliant as a share of Eligible, rounded to the nearest integer.
	// Meaningless when Known is false.
	Percent int
	// Known is false when Eligible is zero: there is nothing to divide by.
	Known bool
	// Eligible is Summary.Zoned: services with a usable zone answer at all.
	Eligible int
	// Compliant is Summary.MultiZone: services spread across more than one zone.
	Compliant int
	// AtRisk is Summary.SingleZone: services sitting in exactly one zone, which lose
	// every replica if that zone goes down.
	AtRisk int
}

// HA folds the zone summary into one percentage: the share of zone-eligible services
// that actually spread across more than one zone.
func (snap *Snapshot) HA() HA {
	h := HA{
		Eligible:  snap.Summary.Zoned,
		Compliant: snap.Summary.MultiZone,
		AtRisk:    snap.Summary.SingleZone,
	}
	if h.Eligible <= 0 {
		return h
	}
	h.Known = true
	h.Percent = roundPercent(h.Compliant, h.Eligible)
	return h
}

// State maps the percentage onto the page's existing colour states, for the gauge's
// colour only: the numeral itself is always shown, never color alone. Mirrors
// Health.State's thresholds exactly.
func (h HA) State() State {
	switch {
	case h.Percent >= 90:
		return StateOK
	case h.Percent >= 75:
		return StateWarning
	default:
		return StateDegraded
	}
}

// NodeHA is HA's counterpart for node spread rather than zone spread: what share of
// node-eligible services actually spread across more than one node.
type NodeHA struct {
	Percent int
	Known   bool
	// Eligible is Summary.SingleNode + Summary.MultiNode: services with a usable node
	// answer at all.
	Eligible int
	// Compliant is Summary.MultiNode: services spread across more than one node.
	Compliant int
	// AtRisk is Summary.SingleNode: services with every running pod on one node.
	AtRisk int
}

// NodeHA folds the node summary into one percentage: the share of node-eligible
// services that actually spread across more than one node.
func (snap *Snapshot) NodeHA() NodeHA {
	n := NodeHA{
		Compliant: snap.Summary.MultiNode,
		AtRisk:    snap.Summary.SingleNode,
	}
	n.Eligible = n.Compliant + n.AtRisk
	if n.Eligible <= 0 {
		return n
	}
	n.Known = true
	n.Percent = roundPercent(n.Compliant, n.Eligible)
	return n
}

// roundPercent rounds num*100/den to the nearest integer and clamps to [0,100].
// Shared by HA and NodeHA so the two percentages behave identically.
func roundPercent(num, den int) int {
	pct := int(math.Round(float64(num) * 100 / float64(den)))
	switch {
	case pct < 0:
		return 0
	case pct > 100:
		return 100
	default:
		return pct
	}
}

// MeshCoverage describes how many of a service's running pods actually carry an
// Istio sidecar, observed the same way ZoneSpread/NodeSpread are: from the running
// pods list already read for AZ_SPREAD, not from any separate mesh-injection read.
//
// This says nothing about mTLS being enforced for the service, only that its pods
// carry the sidecar that makes enforcement possible. The mesh-wide policy answer
// (MeshSection) is the only thing on this page that speaks to enforcement.
type MeshCoverage struct {
	// Pods is how many running pods were matched to this service.
	Pods int
	// Injected is how many of those pods carry an istio-proxy container.
	Injected int
}

// Known reports whether this service has a usable mesh answer at all: at least one
// running pod matched.
func (m MeshCoverage) Known() bool { return m.Pods > 0 }

// Full reports whether every running pod matched to this service carries the Istio
// sidecar.
func (m MeshCoverage) Full() bool { return m.Known() && m.Injected == m.Pods }

// MeshHA is the per-service sidecar-injection counterpart of HA: what share of
// mesh-eligible services have the Istio sidecar on every running pod. Derived
// entirely from Summary, mirroring HA's shape. Zero value (Known false) whenever
// AZ_SPREAD is off, since this reuses that same running-pods read.
type MeshHA struct {
	// Percent is Injected as a share of Eligible, rounded to the nearest integer.
	// Meaningless when Known is false.
	Percent int
	// Known is false when Eligible is zero: there is nothing to divide by.
	Known bool
	// Eligible is Summary.MeshEligible: services with at least one running pod.
	Eligible int
	// Injected is Summary.MeshInjected: services where every running pod carries the
	// Istio sidecar.
	Injected int
}

// MeshHA folds the mesh summary into one percentage: the share of mesh-eligible
// services that have the sidecar on every running pod.
//
// Known is forced false when Istio itself is not installed, even though Eligible
// would otherwise be nonzero (services still have running pods, they simply never
// carry a sidecar to begin with). Without this, a cluster with no service mesh at
// all would show ~100% of its services "not in mesh" in the same warning color as a
// real gap -- correct arithmetic, but the wrong question: there is no mesh to be in.
func (snap *Snapshot) MeshHA() MeshHA {
	m := MeshHA{
		Eligible: snap.Summary.MeshEligible,
		Injected: snap.Summary.MeshInjected,
	}
	if m.Eligible <= 0 || snap.Mesh == nil || !snap.Mesh.Installed {
		return m
	}
	m.Known = true
	m.Percent = roundPercent(m.Injected, m.Eligible)
	return m
}

// NotInMesh is how many mesh-eligible services do not have the sidecar on every
// running pod, the exact population the "not in mesh" tile counts.
func (m MeshHA) NotInMesh() int { return m.Eligible - m.Injected }

// ZoneError is the degraded form of the AZ-spread section: no per-service zone data,
// one note explaining why. Denied and TooLarge each get their own inline hint in the
// template; a failed read here is never swallowed silently the way fetchPending's is.
type ZoneError struct {
	Denied   bool
	Missing  bool
	TooLarge bool
	Error    string
}

// zoneReadError builds the degraded form of the section, following the same shape as
// nodeStatsError and unmanagedError.
func zoneReadError(err error) *ZoneError {
	ze := &ZoneError{Error: truncate(kube.Sanitize(err.Error()), maxTextLen)}
	if errors.Is(err, kube.ErrPodListTooLarge) {
		ze.TooLarge = true
		return ze
	}
	var se *kube.StatusError
	if errors.As(err, &se) {
		ze.Denied = se.Code == http.StatusForbidden || se.Code == http.StatusUnauthorized
		ze.Missing = se.Code == http.StatusNotFound
	}
	return ze
}

// FillZones records, per service, which availability zones its running pods actually
// sit in, and tallies the cluster-wide single/multi-zone split. In the same pass it
// also records each service's mesh sidecar coverage (MeshCoverage): no new fetch, no
// new RBAC, just another fact read off the same running pods.
//
// It joins three lists already fetched elsewhere: running pods (hostIP, container
// statuses), nodes (their InternalIPs and zone label, from PR2), and the pod-to-service
// ownership machinery FillPending already built. No new permission and no new fetch
// beyond the pods read itself.
//
// workloads is the same raw list UNMANAGED already fetches (nil when that feature is
// off). Resiliency is a property of what is actually running, not of who deployed it:
// a workload nobody put through GitOps is exactly as capable of going down with its
// one zone as a tracked service is. Its pods are folded into the same Summary totals
// the HA/NodeHA gauges read, so the percentage covers the whole cluster rather than
// silently excluding whatever isn't in GitOps. They get no per-item Zones/Nodes field
// of their own -- nothing today reads one for a Workload -- only the aggregate counts.
//
// meshNamespace is where the mesh control plane itself lives (istiod's namespace,
// empty when MESH_MTLS is off). Pods there are excluded from mesh-coverage only, not
// from zone/node spread: istiod is the thing that performs sidecar injection, so
// asking whether it injected a sidecar into itself is a nonsense question, not a real
// gap -- flagging it "not in mesh" looked exactly like a real one on a live cluster.
//
// peerAuths is every PeerAuthentication in the cluster (nil when MESH_MTLS is off, or
// when that list read was denied or failed: per-service policy then just never gets
// filled, the same "an optional extra degrades silently" contract FillPending's own
// swallowed read already follows). It resolves each service's own effective mTLS mode
// -- ResolveServicePolicy in mesh.go -- from the namespace and labels of any one of
// its running pods, since all of a workload's pods share both and this is therefore a
// per-service property, not a per-pod one.
func FillZones(snap *Snapshot, pods *kube.PodList, nodes *kube.NodeList, workloads *kube.WorkloadList, meshNamespace string, peerAuths *kube.PeerAuthenticationList) {
	if snap == nil || pods == nil || nodes == nil {
		return
	}

	// Idempotent against a cached snapshot being decorated more than once: everything
	// this function owns is reset before it accumulates anything, the same reason
	// FillGPU resets GPUAlloc instead of +=.
	for i := range snap.Services {
		snap.Services[i].Zones = ZoneSpread{}
		snap.Services[i].Nodes = NodeSpread{}
		snap.Services[i].Mesh = MeshCoverage{}
		snap.Services[i].Policy = ServicePolicy{}
	}
	if snap.Unmanaged != nil {
		for i := range snap.Unmanaged.Items {
			snap.Unmanaged.Items[i].Zones = ZoneSpread{}
			snap.Unmanaged.Items[i].Nodes = NodeSpread{}
			snap.Unmanaged.Items[i].Mesh = MeshCoverage{}
			snap.Unmanaged.Items[i].Policy = ServicePolicy{}
		}
	}
	snap.Summary.Zoned, snap.Summary.SingleZone, snap.Summary.MultiZone = 0, 0, 0
	snap.Summary.SingleNode, snap.Summary.MultiNode = 0, 0
	snap.Summary.MeshEligible, snap.Summary.MeshInjected = 0, 0
	snap.Summary.MeshPolicyEligible, snap.Summary.MeshPolicyPermissive = 0, 0

	// hostIP -> zone, built from every node's InternalIPs(). A node missing a zone
	// label still joins here (its zone is the zoneUnknown sentinel), so a pod that
	// lands on it is placed rather than reported unplaced; zoneUnknown then filters
	// out of Zones below, same as an unmatched hostIP falling into Unplaced.
	zoneByIP := map[string]string{}
	// hostIP -> node name, built from the same InternalIPs() lookup, so a pod's node
	// is known whenever its zone is, and unplaced in exactly the same cases.
	nodeNameByIP := map[string]string{}
	for _, n := range nodes.Items {
		zone := n.Zone()
		if zone == "" {
			zone = zoneUnknown
		}
		for _, ip := range n.InternalIPs() {
			zoneByIP[ip] = zone
			nodeNameByIP[ip] = n.Metadata.Name
		}
	}

	// workload name -> service index, per namespace. Reused verbatim from pending.go:
	// same ownership contract, so a pod is matched to the same service FillPending
	// would blame it on.
	byNS := map[string][]owner{}
	for i := range snap.Services {
		for _, r := range snap.Services[i].Owned {
			switch r.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
				byNS[r.Namespace] = append(byNS[r.Namespace], owner{r.Name, i})
			}
		}
	}

	zonesSeen := make([]map[string]bool, len(snap.Services))
	nodesSeen := make([]map[string]bool, len(snap.Services))
	for i := range zonesSeen {
		zonesSeen[i] = map[string]bool{}
		nodesSeen[i] = map[string]bool{}
	}
	// Namespace and labels of the first running pod matched to each service, used to
	// resolve Policy once per service below. Namespace/labels are a per-workload
	// property, not a per-pod one, so the first pod seen speaks for all of them.
	svcPolicyNS := make([]string, len(snap.Services))
	svcPolicyLabels := make([]map[string]string, len(snap.Services))
	svcPolicySeen := make([]bool, len(snap.Services))

	// A second, parallel index space for workloads no service owns, grouped by the
	// exact same key collapseReleases uses (Release, or a solo key of its own when
	// there is none) -- not by raw item, or a 7-member release would count as 7
	// separate at-risk things instead of the one row the table actually shows.
	var groupKeys []string
	groupIndex := map[string]int{}
	unmanagedByNS := map[string][]owner{}
	if workloads != nil {
		for _, w := range workloads.Items {
			switch w.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
			default:
				continue
			}
			if !isUnmanaged(w) {
				continue
			}
			key := releaseOf(w)
			if key == "" {
				key = "solo:" + w.Metadata.Namespace + "/" + w.Metadata.Name
			}
			idx, seen := groupIndex[key]
			if !seen {
				idx = len(groupKeys)
				groupIndex[key] = idx
				groupKeys = append(groupKeys, key)
			}
			unmanagedByNS[w.Metadata.Namespace] = append(unmanagedByNS[w.Metadata.Namespace], owner{w.Metadata.Name, idx})
		}
	}
	unmanagedZonesSeen := make([]map[string]bool, len(groupKeys))
	unmanagedNodesSeen := make([]map[string]bool, len(groupKeys))
	// Matched pods and unplaced pods, per group -- the same two counts ZoneSpread.Pods
	// and .Unplaced hold for a service, kept separately from the seen-sets above.
	unmanagedPods := make([]int, len(groupKeys))
	unmanagedUnplaced := make([]int, len(groupKeys))
	// Mesh tallies, kept separately from the zone/node seen-sets: injection does not
	// care where a pod landed, only whether it is running at all.
	unmanagedMeshPods := make([]int, len(groupKeys))
	unmanagedMeshInjected := make([]int, len(groupKeys))
	for i := range unmanagedZonesSeen {
		unmanagedZonesSeen[i] = map[string]bool{}
		unmanagedNodesSeen[i] = map[string]bool{}
	}
	// Namespace/labels for Policy resolution, the same per-group convention as the
	// tallies above: one representative pod speaks for the whole group.
	groupPolicyNS := make([]string, len(groupKeys))
	groupPolicyLabels := make([]map[string]string, len(groupKeys))
	groupPolicySeen := make([]bool, len(groupKeys))

	for _, pod := range pods.Items {
		// Defensive: the server-side fieldSelector should already guarantee this, but
		// the fixture HTTP server used by scripts/local-test.sh ignores query strings
		// entirely, so the Pending fixtures would otherwise double-count here too.
		if pod.Status.Phase != "Running" {
			continue
		}
		zone, zoneOK := zoneByIP[pod.Status.HostIP]
		placed := zoneOK && zone != zoneUnknown
		nodeName, nodeOK := nodeNameByIP[pod.Status.HostIP]

		if idx := ownerIndex(byNS[pod.Metadata.Namespace], pod); idx >= 0 {
			svc := &snap.Services[idx]
			svc.Zones.Pods++
			svc.Nodes.Pods++
			if meshNamespace == "" || pod.Metadata.Namespace != meshNamespace {
				svc.Mesh.Pods++
				if pod.IsIstioInjected() {
					svc.Mesh.Injected++
				}
			}
			if !svcPolicySeen[idx] {
				svcPolicySeen[idx] = true
				svcPolicyNS[idx] = pod.Metadata.Namespace
				svcPolicyLabels[idx] = pod.Metadata.Labels
			}
			if nodeOK {
				nodesSeen[idx][nodeName] = true
			}
			if !placed {
				svc.Zones.Unplaced++
				continue
			}
			zonesSeen[idx][zone] = true
			continue
		}
		if uidx := ownerIndex(unmanagedByNS[pod.Metadata.Namespace], pod); uidx >= 0 {
			unmanagedPods[uidx]++
			if meshNamespace == "" || pod.Metadata.Namespace != meshNamespace {
				unmanagedMeshPods[uidx]++
				if pod.IsIstioInjected() {
					unmanagedMeshInjected[uidx]++
				}
			}
			if !groupPolicySeen[uidx] {
				groupPolicySeen[uidx] = true
				groupPolicyNS[uidx] = pod.Metadata.Namespace
				groupPolicyLabels[uidx] = pod.Metadata.Labels
			}
			if nodeOK {
				unmanagedNodesSeen[uidx][nodeName] = true
			}
			if !placed {
				unmanagedUnplaced[uidx]++
				continue
			}
			unmanagedZonesSeen[uidx][zone] = true
		}
	}

	// resolvePolicy is answered once per workload, from whichever running pod's
	// namespace/labels were captured first above. Known() stays false (the zero
	// value) when mesh mTLS is off entirely, since there is then no meshEffective to
	// fall back to and no policy question worth asking.
	resolvePolicy := func(seen bool, namespace string, labels map[string]string) ServicePolicy {
		if !seen || snap.Mesh == nil {
			return ServicePolicy{}
		}
		return ResolveServicePolicy(peerAuths, meshNamespace, namespace, labels, snap.Mesh.Effective)
	}

	for i := range snap.Services {
		if len(zonesSeen[i]) > 0 {
			zones := make([]string, 0, len(zonesSeen[i]))
			for z := range zonesSeen[i] {
				zones = append(zones, z)
			}
			sort.Strings(zones)
			snap.Services[i].Zones.Zones = zones
		}
		if len(nodesSeen[i]) > 0 {
			names := make([]string, 0, len(nodesSeen[i]))
			for n := range nodesSeen[i] {
				names = append(names, n)
			}
			sort.Strings(names)
			snap.Services[i].Nodes.Nodes = names
		}
		snap.Services[i].Policy = resolvePolicy(svcPolicySeen[i], svcPolicyNS[i], svcPolicyLabels[i])
	}

	for _, svc := range snap.Services {
		if svc.Zones.Known() {
			snap.Summary.Zoned++
			if svc.Zones.Count() == 1 {
				snap.Summary.SingleZone++
			} else {
				snap.Summary.MultiZone++
			}
		}
		if svc.Nodes.Known() {
			if svc.Nodes.Count() == 1 {
				snap.Summary.SingleNode++
			} else {
				snap.Summary.MultiNode++
			}
		}
		if svc.Mesh.Known() {
			snap.Summary.MeshEligible++
			if svc.Mesh.Full() {
				snap.Summary.MeshInjected++
			}
		}
		if svc.Policy.Known() {
			snap.Summary.MeshPolicyEligible++
			if svc.Policy.Effective == MeshPermissive {
				snap.Summary.MeshPolicyPermissive++
			}
		}
	}

	// Folded into the exact same totals as the loop above, not a separate count: the
	// HA/NodeHA gauges read Summary directly, so this is what makes them describe the
	// whole cluster's resiliency rather than only its GitOps-tracked slice of it.
	//
	// Also recorded per group key (not just tallied) so the badge below can show each
	// row exactly what made it single- or multi-zone, the same way a service's own
	// Zones field does.
	zonesByKey := make(map[string]ZoneSpread, len(groupKeys))
	nodesByKey := make(map[string]NodeSpread, len(groupKeys))
	meshByKey := make(map[string]MeshCoverage, len(groupKeys))
	policyByKey := make(map[string]ServicePolicy, len(groupKeys))
	for i, key := range groupKeys {
		if unmanagedMeshPods[i] > 0 {
			mc := MeshCoverage{Pods: unmanagedMeshPods[i], Injected: unmanagedMeshInjected[i]}
			meshByKey[key] = mc
			snap.Summary.MeshEligible++
			if mc.Full() {
				snap.Summary.MeshInjected++
			}
		}
		if policy := resolvePolicy(groupPolicySeen[i], groupPolicyNS[i], groupPolicyLabels[i]); policy.Known() {
			policyByKey[key] = policy
			snap.Summary.MeshPolicyEligible++
			if policy.Effective == MeshPermissive {
				snap.Summary.MeshPolicyPermissive++
			}
		}
		if len(unmanagedZonesSeen[i]) > 0 {
			snap.Summary.Zoned++
			zones := make([]string, 0, len(unmanagedZonesSeen[i]))
			for z := range unmanagedZonesSeen[i] {
				zones = append(zones, z)
			}
			sort.Strings(zones)
			zonesByKey[key] = ZoneSpread{Zones: zones, Pods: unmanagedPods[i], Unplaced: unmanagedUnplaced[i]}
			if len(zones) == 1 {
				snap.Summary.SingleZone++
			} else {
				snap.Summary.MultiZone++
			}
		}
		if len(unmanagedNodesSeen[i]) > 0 {
			names := make([]string, 0, len(unmanagedNodesSeen[i]))
			for n := range unmanagedNodesSeen[i] {
				names = append(names, n)
			}
			sort.Strings(names)
			nodesByKey[key] = NodeSpread{Nodes: names, Pods: unmanagedPods[i]}
			if len(names) == 1 {
				snap.Summary.SingleNode++
			} else {
				snap.Summary.MultiNode++
			}
		}
	}

	// Re-derive each already-collapsed row's own group key to look itself up above.
	// Release is never rewritten by collapseReleases, so it is a stable join key for
	// a release row; a solo row's Namespace/Name are untouched by collapsing at all.
	if snap.Unmanaged != nil {
		for i := range snap.Unmanaged.Items {
			w := &snap.Unmanaged.Items[i]
			key := w.Release
			if key == "" {
				key = "solo:" + w.Namespace + "/" + w.Name
			}
			w.Zones = zonesByKey[key]
			w.Nodes = nodesByKey[key]
			w.Mesh = meshByKey[key]
			w.Policy = policyByKey[key]
		}
	}
}

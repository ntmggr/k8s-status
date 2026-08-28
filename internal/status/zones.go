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
// sit in, and tallies the cluster-wide single/multi-zone split.
//
// It joins three lists already fetched elsewhere: running pods (hostIP), nodes (their
// InternalIPs and zone label, from PR2), and the pod-to-service ownership machinery
// FillPending already built. No new permission and no new fetch beyond the pods read
// itself.
//
// workloads is the same raw list UNMANAGED already fetches (nil when that feature is
// off). Resiliency is a property of what is actually running, not of who deployed it:
// a workload nobody put through GitOps is exactly as capable of going down with its
// one zone as a tracked service is. Its pods are folded into the same Summary totals
// the HA/NodeHA gauges read, so the percentage covers the whole cluster rather than
// silently excluding whatever isn't in GitOps. They get no per-item Zones/Nodes field
// of their own -- nothing today reads one for a Workload -- only the aggregate counts.
func FillZones(snap *Snapshot, pods *kube.PodList, nodes *kube.NodeList, workloads *kube.WorkloadList) {
	if snap == nil || pods == nil || nodes == nil {
		return
	}

	// Idempotent against a cached snapshot being decorated more than once: everything
	// this function owns is reset before it accumulates anything, the same reason
	// FillGPU resets GPUAlloc instead of +=.
	for i := range snap.Services {
		snap.Services[i].Zones = ZoneSpread{}
		snap.Services[i].Nodes = NodeSpread{}
	}
	snap.Summary.Zoned, snap.Summary.SingleZone, snap.Summary.MultiZone = 0, 0, 0
	snap.Summary.SingleNode, snap.Summary.MultiNode = 0, 0

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

	// A second, parallel index space for workloads no service owns. Not stored on
	// anything -- Workload has no Zones/Nodes field, and nothing renders one -- only
	// folded into the same Summary totals the GitOps services above contribute to.
	var unmanagedItems int
	unmanagedByNS := map[string][]owner{}
	if workloads != nil {
		unmanagedItems = len(workloads.Items)
		for i, w := range workloads.Items {
			switch w.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
			default:
				continue
			}
			if !isUnmanaged(w) {
				continue
			}
			unmanagedByNS[w.Metadata.Namespace] = append(unmanagedByNS[w.Metadata.Namespace], owner{w.Metadata.Name, i})
		}
	}
	unmanagedZonesSeen := make([]map[string]bool, unmanagedItems)
	unmanagedNodesSeen := make([]map[string]bool, unmanagedItems)
	for i := range unmanagedZonesSeen {
		unmanagedZonesSeen[i] = map[string]bool{}
		unmanagedNodesSeen[i] = map[string]bool{}
	}

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
			if nodeOK {
				unmanagedNodesSeen[uidx][nodeName] = true
			}
			if placed {
				unmanagedZonesSeen[uidx][zone] = true
			}
		}
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
	}

	// Folded into the exact same totals as the loop above, not a separate count: the
	// HA/NodeHA gauges read Summary directly, so this is what makes them describe the
	// whole cluster's resiliency rather than only its GitOps-tracked slice of it.
	for i := range unmanagedZonesSeen {
		if len(unmanagedZonesSeen[i]) > 0 {
			snap.Summary.Zoned++
			if len(unmanagedZonesSeen[i]) == 1 {
				snap.Summary.SingleZone++
			} else {
				snap.Summary.MultiZone++
			}
		}
		if len(unmanagedNodesSeen[i]) > 0 {
			if len(unmanagedNodesSeen[i]) == 1 {
				snap.Summary.SingleNode++
			} else {
				snap.Summary.MultiNode++
			}
		}
	}
}

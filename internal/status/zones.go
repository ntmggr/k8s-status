package status

import (
	"errors"
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
func FillZones(snap *Snapshot, pods *kube.PodList, nodes *kube.NodeList) {
	if snap == nil || pods == nil || nodes == nil {
		return
	}

	// Idempotent against a cached snapshot being decorated more than once: everything
	// this function owns is reset before it accumulates anything, the same reason
	// FillGPU resets GPUAlloc instead of +=.
	for i := range snap.Services {
		snap.Services[i].Zones = ZoneSpread{}
	}
	snap.Summary.Zoned, snap.Summary.SingleZone, snap.Summary.MultiZone = 0, 0, 0

	// hostIP -> zone, built from every node's InternalIPs(). A node missing a zone
	// label still joins here (its zone is the zoneUnknown sentinel), so a pod that
	// lands on it is placed rather than reported unplaced; zoneUnknown then filters
	// out of Zones below, same as an unmatched hostIP falling into Unplaced.
	zoneByIP := map[string]string{}
	for _, n := range nodes.Items {
		zone := n.Zone()
		if zone == "" {
			zone = zoneUnknown
		}
		for _, ip := range n.InternalIPs() {
			zoneByIP[ip] = zone
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
	for i := range zonesSeen {
		zonesSeen[i] = map[string]bool{}
	}

	for _, pod := range pods.Items {
		// Defensive: the server-side fieldSelector should already guarantee this, but
		// the fixture HTTP server used by scripts/local-test.sh ignores query strings
		// entirely, so the Pending fixtures would otherwise double-count here too.
		if pod.Status.Phase != "Running" {
			continue
		}
		idx := ownerIndex(byNS[pod.Metadata.Namespace], pod)
		if idx < 0 {
			continue
		}
		svc := &snap.Services[idx]
		svc.Zones.Pods++
		zone, ok := zoneByIP[pod.Status.HostIP]
		if !ok || zone == zoneUnknown {
			svc.Zones.Unplaced++
			continue
		}
		zonesSeen[idx][zone] = true
	}

	for i := range snap.Services {
		if len(zonesSeen[i]) == 0 {
			continue
		}
		zones := make([]string, 0, len(zonesSeen[i]))
		for z := range zonesSeen[i] {
			zones = append(zones, z)
		}
		sort.Strings(zones)
		snap.Services[i].Zones.Zones = zones
	}

	for _, svc := range snap.Services {
		if !svc.Zones.Known() {
			continue
		}
		snap.Summary.Zoned++
		if svc.Zones.Count() == 1 {
			snap.Summary.SingleZone++
		} else {
			snap.Summary.MultiZone++
		}
	}
}

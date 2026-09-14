package status

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// ArchCount is one entry of the node architecture split.
type ArchCount struct {
	Arch  string
	Count int
}

// NodeStats describes the cluster's compute, not the health of any service.
// Error is set when the nodes read failed; the rest of the page is unaffected.
type NodeStats struct {
	Total int
	// NotReady counts nodes the kubelet is not reporting as ready, including ones the
	// cloud provider has shut down but not yet removed. They still appear in the node
	// list, so leaving them out of the total would overstate nothing but hide them.
	NotReady int
	CPUNodes int
	GPUNodes int
	// GPUs counts allocatable devices only. Without a device plugin the number of
	// cards is not discoverable at all, so this is zero even where GPUNodes is not,
	// and the page says so rather than printing a bare "0 cards".
	GPUs int
	// Accelerators breaks that total down per resource, so a cluster with MIG slices
	// or more than one vendor can see which is which. One entry on a typical cluster.
	Accelerators []AcceleratorCount
	// GPUsAllocated is how many of GPUs the scheduler has actually handed to a
	// workload. The gap between the two is idle hardware.
	GPUsAllocated int
	// UnschedulableGPUNodes is the subset of GPUNodes that advertise nothing, meaning
	// no device plugin. Workloads there still reach the GPU through the runtime, but
	// the scheduler cannot allocate or limit it, so any number of pods can land on one
	// card and fight over its memory. Worth showing rather than silently reporting zero.
	UnschedulableGPUNodes int
	Arch                  []ArchCount
	// Zones is sorted by zone name; nodes with no zone label bucket into a single
	// zoneUnknown entry rather than being dropped from the tally.
	Zones []ZoneCount
	// KubernetesVersion is the cluster's own major.minor (e.g. "1.32"), read from a
	// Ready node's kubelet version where one exists, falling back to any node
	// otherwise. Empty when there are no nodes to read it from.
	KubernetesVersion string
	// Provider names the cloud and, where the kubelet version or ProviderID gives a
	// reliable signal, the managed offering on it (e.g. "AWS EKS"). Empty when
	// neither gives one -- see detectProvider.
	Provider string
	// Rows is one entry per node, worst-first (not-ready nodes before ready ones,
	// then by name), for the per-node table. Nil on the same Denied/Error/no-data
	// paths as everything else here.
	Rows   []NodeRow
	Denied bool
	Error  string
}

// NodeRow is one row of the per-node table: enough to spot a problem node without
// needing kubectl, using only fields this project already reads for the aggregate
// counts above.
type NodeRow struct {
	Name  string
	Zone  string
	Arch  string
	Ready bool
	// CPU and Memory are allocatable, not capacity: what a pod can actually request,
	// which is what a reader comparing this to a workload's own requests wants.
	CPU    string
	Memory string
	// GPUResource is empty for a node with no accelerator. GPU is that resource's
	// allocatable count.
	GPUResource string
	GPU         int
}

// AcceleratorCount is one device type and how much of it the cluster has.
type AcceleratorCount struct {
	Resource string
	Nodes    int
	Count    int
}

// ZoneCount is one availability zone and how many of the cluster's nodes are in it.
type ZoneCount struct {
	Zone  string
	Nodes int
	Ready int
}

const archUnknown = "unknown"
const zoneUnknown = "unknown"

// BuildNodeStats classifies nodes by accelerator capacity and tallies architectures.
// accel is the resource list to count, normally from DiscoverAccelerators.
func BuildNodeStats(list *kube.NodeList, accel []string) NodeStats {
	stats := NodeStats{}
	if list == nil {
		return stats
	}
	byArch := map[string]int{}
	perRes := map[string][2]int{} // resource -> {nodes, devices}
	byZone := map[string][2]int{} // zone -> {nodes, ready}
	// versionSource/providerID are read from the first Ready node found, preferred
	// over a NotReady one since a node the cloud has shut down can be stale --
	// mid-upgrade, or left over from a previous cluster version entirely.
	var versionSource, providerID string
	haveReadySource := false
	for _, n := range list.Items {
		stats.Total++
		ready := n.Ready()
		if !ready {
			stats.NotReady++
		}
		if versionSource == "" || (ready && !haveReadySource) {
			versionSource = n.Status.NodeInfo.KubeletVersion
			providerID = n.Spec.ProviderID
			haveReadySource = ready
		}

		// A node with a device is a GPU node whether or not the scheduler can hand it
		// out. Counting only allocatable ones reports "0 gpu" on a cluster whose GPUs
		// are busy serving traffic, which is the most misleading thing the page could
		// say about it.
		gpus := nodeAccelerators(n, accel)
		hardware := hasAcceleratorHardware(n)
		switch {
		case gpus > 0:
			stats.GPUNodes++
			stats.GPUs += gpus
		case hardware:
			stats.GPUNodes++
			stats.UnschedulableGPUNodes++
		default:
			stats.CPUNodes++
		}
		for _, name := range accel {
			if c := quantityInt(string(n.Status.Capacity[name])); c > 0 {
				e := perRes[name]
				perRes[name] = [2]int{e[0] + 1, e[1] + c}
			}
		}

		arch := strings.TrimSpace(n.Status.NodeInfo.Architecture)
		if arch == "" {
			arch = archUnknown
		}
		byArch[arch]++

		zone := n.Zone()
		if zone == "" {
			zone = zoneUnknown
		}
		e := byZone[zone]
		e[0]++
		if ready {
			e[1]++
		}
		byZone[zone] = e

		row := NodeRow{
			Name:   n.Metadata.Name,
			Zone:   n.Zone(),
			Arch:   n.Status.NodeInfo.Architecture,
			Ready:  ready,
			CPU:    formatCPUCores(parseCPUCores(n.Status.Allocatable["cpu"])),
			Memory: formatMemoryGiB(parseMemoryBytes(n.Status.Allocatable["memory"])),
		}
		for _, name := range accel {
			if c := quantityInt(string(n.Status.Allocatable[name])); c > 0 {
				row.GPUResource, row.GPU = name, c
				break
			}
		}
		stats.Rows = append(stats.Rows, row)
	}

	stats.Accelerators = make([]AcceleratorCount, 0, len(perRes))
	for _, name := range accel {
		if e, ok := perRes[name]; ok {
			stats.Accelerators = append(stats.Accelerators, AcceleratorCount{Resource: name, Nodes: e[0], Count: e[1]})
		}
	}

	stats.Arch = make([]ArchCount, 0, len(byArch))
	for a, c := range byArch {
		stats.Arch = append(stats.Arch, ArchCount{Arch: a, Count: c})
	}
	sort.Slice(stats.Arch, func(i, j int) bool {
		if stats.Arch[i].Count != stats.Arch[j].Count {
			return stats.Arch[i].Count > stats.Arch[j].Count
		}
		return stats.Arch[i].Arch < stats.Arch[j].Arch
	})

	stats.Zones = make([]ZoneCount, 0, len(byZone))
	for z, e := range byZone {
		stats.Zones = append(stats.Zones, ZoneCount{Zone: z, Nodes: e[0], Ready: e[1]})
	}
	sort.Slice(stats.Zones, func(i, j int) bool { return stats.Zones[i].Zone < stats.Zones[j].Zone })
	// Not-ready nodes first -- the ones worth a look -- then alphabetically, same
	// worst-first convention the services table sorts by.
	sort.Slice(stats.Rows, func(i, j int) bool {
		if stats.Rows[i].Ready != stats.Rows[j].Ready {
			return !stats.Rows[i].Ready
		}
		return stats.Rows[i].Name < stats.Rows[j].Name
	})
	stats.KubernetesVersion = parseKubernetesVersion(versionSource)
	stats.Provider = detectProvider(providerID, versionSource)
	return stats
}

// parseKubernetesVersion reduces a kubelet version like "v1.32.3-eks-be96eb4" to
// "1.32": patch versions can differ node to node during a rolling upgrade, so the
// coarser major.minor is the more stable answer to "what does this cluster run".
func parseKubernetesVersion(kubeletVersion string) string {
	v := strings.TrimPrefix(kubeletVersion, "v")
	if v == "" {
		return ""
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// detectProvider names the cloud and, where the kubelet version or ProviderID gives
// a reliable signal, the managed offering on it. Returns "" rather than guess when
// neither gives one -- bare metal, kind, minikube and similar have no such marker.
func detectProvider(providerID, kubeletVersion string) string {
	switch {
	case strings.HasPrefix(providerID, "aws:"):
		if strings.Contains(kubeletVersion, "-eks-") {
			return "AWS EKS"
		}
		return "AWS"
	case strings.HasPrefix(providerID, "azure:"):
		// AKS's own auto-generated node resource group is always named
		// MC_<resourceGroup>_<clusterName>_<region> -- a reliable AKS signature,
		// since ProviderID alone doesn't distinguish AKS from a self-managed
		// cluster on Azure VMs.
		if strings.Contains(providerID, "/resourceGroups/MC_") {
			return "Azure AKS"
		}
		return "Azure"
	case strings.HasPrefix(providerID, "gce:"):
		if strings.Contains(kubeletVersion, "-gke.") {
			return "Google GKE"
		}
		return "Google Cloud"
	default:
		return ""
	}
}

// quantityInt reads a whole-unit resource quantity; anything unparseable counts as zero
// rather than failing the page.
func quantityInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// nodeStatsError builds the degraded form of the section: no counts, one note.
func nodeStatsError(err error) NodeStats {
	var stats NodeStats
	stats.Error = truncate(kube.Sanitize(err.Error()), maxTextLen)
	var se *kube.StatusError
	if errors.As(err, &se) {
		stats.Denied = se.Code == http.StatusForbidden || se.Code == http.StatusUnauthorized
	}
	return stats
}

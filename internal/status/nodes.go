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
	Total    int
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
	Denied                bool
	Error                 string
}

// AcceleratorCount is one device type and how much of it the cluster has.
type AcceleratorCount struct {
	Resource string
	Nodes    int
	Count    int
}

const archUnknown = "unknown"

// BuildNodeStats classifies nodes by accelerator capacity and tallies architectures.
// accel is the resource list to count, normally from DiscoverAccelerators.
func BuildNodeStats(list *kube.NodeList, accel []string) NodeStats {
	stats := NodeStats{}
	if list == nil {
		return stats
	}
	byArch := map[string]int{}
	perRes := map[string][2]int{} // resource -> {nodes, devices}
	for _, n := range list.Items {
		stats.Total++

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
	return stats
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

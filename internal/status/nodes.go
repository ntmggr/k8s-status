package status

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ntmggr/srv-status/internal/kube"
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
	GPUs     int
	Arch     []ArchCount
	Denied   bool
	Error    string
}

const archUnknown = "unknown"

// BuildNodeStats classifies nodes by GPU capacity and tallies architectures.
func BuildNodeStats(list *kube.NodeList) NodeStats {
	stats := NodeStats{}
	if list == nil {
		return stats
	}
	byArch := map[string]int{}
	for _, n := range list.Items {
		stats.Total++

		gpus := quantityInt(string(n.Status.Capacity.NvidiaGPU))
		if gpus > 0 {
			stats.GPUNodes++
			stats.GPUs += gpus
		} else {
			stats.CPUNodes++
		}

		arch := strings.TrimSpace(n.Status.NodeInfo.Architecture)
		if arch == "" {
			arch = archUnknown
		}
		byArch[arch]++
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

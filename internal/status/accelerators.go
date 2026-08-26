package status

import (
	"sort"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// nonAcceleratorPrefixes are vendor-qualified resources that are not devices. EKS
// advertises network attachments this way when security groups for pods are enabled,
// and counting those would mark every node in such a cluster as accelerated.
//
// This is a deny list rather than an allow list on purpose: an allow list would have
// to name every vendor, and would silently miss the next one. ACCELERATOR_RESOURCES
// replaces discovery outright when a cluster needs something this does not cover.
var nonAcceleratorPrefixes = []string{
	"vpc.amazonaws.com/", // pod-eni, private-ip-address
}

// DiscoverAccelerators returns the accelerator resources the cluster advertises,
// sorted for stable output. An explicit override wins and skips discovery entirely.
//
// Kubernetes requires an extended resource to be vendor-qualified, so anything without
// a slash is a built-in: cpu, memory, pods, ephemeral-storage, attachable-volumes-*.
// hugepages-* is qualified in spirit but not in form, and is excluded by name.
func DiscoverAccelerators(list *kube.NodeList, override []string) []string {
	if len(override) > 0 {
		out := append([]string(nil), override...)
		sort.Strings(out)
		return out
	}
	if list == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, n := range list.Items {
		for name, q := range n.Status.Capacity {
			if !isAcceleratorResource(name) {
				continue
			}
			// A resource advertised as zero everywhere is not worth a column.
			if quantityInt(string(q)) > 0 {
				seen[name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func isAcceleratorResource(name string) bool {
	if !strings.Contains(name, "/") || strings.HasPrefix(name, "hugepages-") {
		return false
	}
	for _, p := range nonAcceleratorPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// nodeAccelerators totals the accelerators one node advertises.
func nodeAccelerators(n kube.Node, accel []string) int {
	total := 0
	for _, name := range accel {
		total += quantityInt(string(n.Status.Capacity[name]))
	}
	return total
}

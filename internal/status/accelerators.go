package status

import (
	"sort"
	"strconv"
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

// AcceleratorLabel is the noun the page uses for accelerators: "gpu" on an ordinary
// NVIDIA or AMD cluster, "neuron" or "tpu" where that is what is installed, and
// "accelerators" when a cluster runs more than one kind.
//
// Derived from the resource name rather than looked up in a vendor table, so a device
// that does not exist yet still reads correctly.
func (s *Snapshot) AcceleratorLabel() string {
	if s == nil || s.Nodes == nil || len(s.Nodes.Accelerators) == 0 {
		return "gpu"
	}
	if len(s.Nodes.Accelerators) > 1 {
		return "accelerators"
	}
	return acceleratorNoun(s.Nodes.Accelerators[0].Resource)
}

// AcceleratorBadge is the same word as a short uppercase row badge. "ACCELERATORS"
// would not fit beside a service name, so a mixed cluster gets "ACCEL".
func (s *Snapshot) AcceleratorBadge() string {
	label := s.AcceleratorLabel()
	if label == "accelerators" {
		return "ACCEL"
	}
	return strings.ToUpper(label)
}

// AcceleratorUnit names what is being counted. NVIDIA and AMD sell cards; a MIG slice
// or a Neuron core is not one, so anything else is called a device.
func (s *Snapshot) AcceleratorUnit() string {
	if s.AcceleratorLabel() == "gpu" {
		return "cards"
	}
	return "devices"
}

// AcceleratorDetail spells out the breakdown for a tooltip, so the total is traceable
// to the resources it came from.
func (s *Snapshot) AcceleratorDetail() string {
	if s == nil || s.Nodes == nil || len(s.Nodes.Accelerators) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Nodes.Accelerators))
	for _, a := range s.Nodes.Accelerators {
		parts = append(parts, a.Resource+" "+strconv.Itoa(a.Count))
	}
	return strings.Join(parts, ", ")
}

// acceleratorNoun reduces a resource name to a word: the part after the vendor domain,
// with any variant suffix trimmed, so nvidia.com/mig-1g.5gb reads as "mig".
func acceleratorNoun(res string) string {
	name := res
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.Index(name, "-"); i > 0 {
		name = name[:i]
	}
	if name == "" {
		return "gpu"
	}
	return name
}

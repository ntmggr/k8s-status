package status

import (
	"strconv"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// FillGPU marks the services that actually ask for a GPU, read from the workloads
// each Application owns rather than guessed from its name.
//
// Name patterns do not survive contact with a real cluster. They cannot tell a GPU
// service from a CPU one with a similar name, they keep flagging services that were
// moved off GPU nodes, and they need a per-site list that is wrong everywhere else.
// A request for nvidia.com/gpu is the cluster's own answer to the same question.
//
// Two signals, because one is not enough on a real cluster:
//
//   - the workload requests nvidia.com/gpu, or
//   - every node the workload is allowed to run on has a GPU.
//
// The second matters because a workload can be given a whole GPU node through node
// affinity and talk to the device directly, never naming the resource. Three services
// on the cluster this was built against do exactly that.
//
// Both signals read the spec, not running pods, so a service scaled to zero is still
// reported and a DaemonSet that merely lands on a GPU node is not: its rules also
// match nodes without one.
//
// Like FillMissingVersions this reuses the workload list already fetched for the
// unmanaged view, so it costs no extra request and no extra permission. When that
// list is absent, GPU marking falls back to whatever GPU_GLOBS was set to, which is
// nothing unless the operator chose otherwise.
func FillGPU(snap *Snapshot, list *kube.WorkloadList, nodes *kube.NodeList, accel []string) {
	if snap == nil || list == nil {
		return
	}

	type key struct{ kind, ns, name string }
	var nodeItems []kube.Node
	if nodes != nil {
		nodeItems = nodes.Items
	}

	owners := make(map[key]bool, len(list.Items))
	for _, w := range list.Items {
		if workloadGPUs(w, accel) > 0 || onlyRunsOnGPUNodes(w, nodeItems, accel) {
			owners[key{w.Kind, w.Metadata.Namespace, w.Metadata.Name}] = true
		}
	}

	for i := range snap.Services {
		svc := &snap.Services[i]
		for _, r := range svc.Owned {
			switch r.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
			default:
				continue
			}
			if owners[key{r.Kind, r.Namespace, r.Name}] {
				svc.GPU = true
				break
			}
		}
	}

	// addService tallied this while building, before any workload was known.
	snap.Summary.GPU = 0
	for _, svc := range snap.Services {
		if svc.GPU {
			snap.Summary.GPU++
		}
	}
}

// workloadGPUs returns the GPUs one pod of this workload asks for. Limits win over
// requests because that is the value the scheduler enforces for an extended resource,
// and a GPU request without a limit is not valid anyway.
func workloadGPUs(w kube.Workload, accel []string) int {
	total := 0
	for _, c := range w.Spec.Template.Spec.Containers {
		for _, name := range accel {
			q := c.Resources.Limits[name]
			if q == "" {
				q = c.Resources.Requests[name]
			}
			if n, err := strconv.Atoi(strings.TrimSpace(q)); err == nil && n > 0 {
				total += n
			}
		}
	}
	return total
}

// onlyRunsOnGPUNodes reports whether every node this workload may be scheduled onto
// carries a GPU. "Every" rather than "any" is what keeps a DaemonSet out: its rules
// also select nodes without one, so it cannot be claimed as GPU-backed.
func onlyRunsOnGPUNodes(w kube.Workload, nodes []kube.Node, accel []string) bool {
	if len(nodes) == 0 {
		return false
	}
	matched, withGPU := 0, 0
	for _, n := range nodes {
		if !schedulable(w.Spec.Template.Spec, n) {
			continue
		}
		matched++
		if nodeAccelerators(n, accel) > 0 {
			withGPU++
		}
	}
	return matched > 0 && matched == withGPU
}

func schedulable(ps kube.PodSpec, n kube.Node) bool {
	labels := n.Metadata.Labels
	for k, v := range ps.NodeSelector {
		if labels[k] != v {
			return false
		}
	}
	if ps.Affinity == nil || ps.Affinity.NodeAffinity == nil || ps.Affinity.NodeAffinity.Required == nil {
		return true
	}
	terms := ps.Affinity.NodeAffinity.Required.NodeSelectorTerms
	if len(terms) == 0 {
		return true
	}
	for _, t := range terms { // terms are ORed
		if termMatches(t, labels) {
			return true
		}
	}
	return false
}

// termMatches evaluates one nodeSelectorTerm. Expressions within a term are ANDed.
// Anything it cannot evaluate, including matchFields and the Gt/Lt operators, counts
// as no match: guessing here would invent GPU services.
func termMatches(t kube.NodeSelectorTerm, labels map[string]string) bool {
	if len(t.MatchFields) > 0 {
		return false
	}
	for _, e := range t.MatchExpressions {
		v, has := labels[e.Key]
		switch e.Operator {
		case "In":
			if !has || !contains(e.Values, v) {
				return false
			}
		case "NotIn":
			if has && contains(e.Values, v) {
				return false
			}
		case "Exists":
			if !has {
				return false
			}
		case "DoesNotExist":
			if has {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

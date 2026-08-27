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

	type detail struct {
		gpu            bool
		perReplica     int
		desired, ready int
	}
	owners := make(map[key]detail, len(list.Items))
	for _, w := range list.Items {
		g := workloadGPUs(w, accel)
		if g > 0 || onlyRunsOnGPUNodes(w, nodeItems, accel) {
			d, r := workloadReplicas(w)
			owners[key{w.Kind, w.Metadata.Namespace, w.Metadata.Name}] = detail{
				gpu: true, perReplica: g, desired: d, ready: r,
			}
		}
	}

	for i := range snap.Services {
		svc := &snap.Services[i]
		// Reset before summing. A cached snapshot is decorated again on the stale
		// path, and its Services share a backing array with the cached copy, so a
		// bare += would keep adding to yesterday's totals: a workload asking for one
		// device reported two.
		svc.GPUAlloc = GPUAllocation{}
		// A service can own more than one workload, so the totals are summed rather
		// than taken from the first match.
		for _, r := range svc.Owned {
			switch r.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
			default:
				continue
			}
			d, ok := owners[key{r.Kind, r.Namespace, r.Name}]
			if !ok || !d.gpu {
				continue
			}
			svc.GPU = true
			svc.GPUAlloc.PerReplica += d.perReplica
			svc.GPUAlloc.Desired += d.desired
			svc.GPUAlloc.Ready += d.ready
			svc.GPUAlloc.Allocated += d.perReplica * d.ready
		}
	}

	// addService tallied this while building, before any workload was known.
	snap.Summary.GPU = 0
	allocated, running, waiting, stopped, unmeasured := 0, 0, 0, 0, 0
	for _, svc := range snap.Services {
		if !svc.GPU {
			continue
		}
		snap.Summary.GPU++
		allocated += svc.GPUAlloc.Allocated
		if svc.GPUAlloc.Unmeasured() {
			unmeasured++
		}
		switch {
		case svc.GPUAlloc.Waiting():
			waiting++
		case svc.GPUAlloc.ScaledToZero():
			stopped++
		default:
			running++
		}
	}
	snap.Summary.GPUWaiting = waiting
	snap.Summary.GPURunning = running
	snap.Summary.GPUUnmeasured = unmeasured
	snap.Summary.GPUStopped = stopped
	if snap.Nodes != nil {
		snap.Nodes.GPUsAllocated = allocated
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
// carries a device, counting hardware the cluster cannot schedule as well as capacity
// it can. A cluster without a device plugin still runs GPU workloads. "Every" rather than "any" is what keeps a DaemonSet out: its rules
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
		if nodeHasAccelerator(n, accel) {
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

// workloadReplicas returns desired and ready counts across the three kinds. Deployment
// and StatefulSet report spec.replicas and status.readyReplicas; a DaemonSet reports
// desiredNumberScheduled and numberReady instead.
func workloadReplicas(w kube.Workload) (desired, ready int) {
	if w.Kind == "DaemonSet" {
		return w.Status.DesiredNumberScheduled, w.Status.NumberReady
	}
	desired = w.Status.Replicas
	if w.Spec.Replicas != nil {
		// spec.replicas is the intent; status.replicas lags during a scale-down and
		// would make a service parked at zero look like it is still trying.
		desired = *w.Spec.Replicas
	}
	return desired, w.Status.ReadyReplicas
}

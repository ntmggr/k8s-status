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
// Detection is by container resources rather than by which node a pod landed on: a
// DaemonSet scheduled onto a GPU node is not a GPU workload, and a service scaled to
// zero still is one.
//
// Like FillMissingVersions this reuses the workload list already fetched for the
// unmanaged view, so it costs no extra request and no extra permission. When that
// list is absent, GPU marking falls back to whatever GPU_GLOBS was set to, which is
// nothing unless the operator chose otherwise.
func FillGPU(snap *Snapshot, list *kube.WorkloadList) {
	if snap == nil || list == nil {
		return
	}

	type key struct{ kind, ns, name string }
	owners := make(map[key]int, len(list.Items))
	for _, w := range list.Items {
		if n := workloadGPUs(w); n > 0 {
			owners[key{w.Kind, w.Metadata.Namespace, w.Metadata.Name}] = n
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
			if n, ok := owners[key{r.Kind, r.Namespace, r.Name}]; ok && n > 0 {
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
func workloadGPUs(w kube.Workload) int {
	total := 0
	for _, c := range w.Spec.Template.Spec.Containers {
		q := c.Resources.Limits[kube.ResourceGPU]
		if q == "" {
			q = c.Resources.Requests[kube.ResourceGPU]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(q)); err == nil && n > 0 {
			total += n
		}
	}
	return total
}

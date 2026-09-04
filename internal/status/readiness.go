package status

import "github.com/ntmggr/k8s-status/internal/kube"

// Readiness is a service's own ready/desired pod count, summed across every
// Deployment/StatefulSet/DaemonSet it owns -- the same per-object counters the
// not-in-gitops table's own Ready column reads, via the same readiness() helper.
type Readiness struct {
	Ready, Desired int
	// Found is true when at least one of this service's owned workloads was matched
	// in the workload list. False means UNMANAGED is off, or ArgoCD reported no
	// Deployment/StatefulSet/DaemonSet for this service at all (e.g. a ConfigMap-only
	// Application). Desired == 0 alone cannot mean this: a DaemonSet whose node
	// selector matches nothing is legitimately 0/0, same as the unmanaged table's own
	// SUSPENDED case.
	Found bool
}

// FillReadiness matches each service's owned Deployment/StatefulSet/DaemonSet
// components (from ArgoCD's own resource tree, via Owned) to the actual workload
// objects, by name and namespace -- no owner-reference indirection, the same reason
// FillJobs needs none.
func FillReadiness(snap *Snapshot, workloads *kube.WorkloadList) {
	if snap == nil {
		return
	}
	for i := range snap.Services {
		snap.Services[i].PodsReady = Readiness{}
	}
	if workloads == nil {
		return
	}

	type key struct{ ns, name string }
	byWorkload := map[key]kube.Workload{}
	for _, w := range workloads.Items {
		switch w.Kind {
		case kube.KindDeployment, kube.KindStatefulSet, kube.KindDaemonSet:
			byWorkload[key{w.Metadata.Namespace, w.Metadata.Name}] = w
		}
	}

	for i := range snap.Services {
		var r Readiness
		for _, o := range snap.Services[i].Owned {
			switch o.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
			default:
				continue
			}
			w, ok := byWorkload[key{o.Namespace, o.Name}]
			if !ok {
				continue
			}
			ready, desired := readiness(w)
			r.Ready += ready
			r.Desired += desired
			r.Found = true
		}
		snap.Services[i].PodsReady = r
	}
}

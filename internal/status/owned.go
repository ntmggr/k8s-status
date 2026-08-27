package status

import "github.com/ntmggr/k8s-status/internal/kube"

// FillOwnedFromLabels supplies a service's workloads when its Application does not
// report any.
//
// ArgoCD normally lists what it owns in status.resources, and everything downstream
// joins on that. An Application it cannot introspect, one whose sync status is Unknown,
// lists nothing at all, and that service then loses its version, its accelerator
// marking, its architecture badge and any explanation of why its pods are stuck. It is
// not that the workloads are missing; it is that ArgoCD did not say which they are.
//
// The workloads themselves carry the answer. ArgoCD labels everything it tracks with
// app.kubernetes.io/instance set to the Application name, so the mapping can be read
// back off the objects already fetched for the unmanaged view.
//
// Only used as a fallback. An Application that reports its own resources is believed,
// because the label is also written by Helm and a name could coincide.
func FillOwnedFromLabels(snap *Snapshot, list *kube.WorkloadList) {
	if snap == nil || list == nil {
		return
	}

	missing := map[string]int{}
	for i := range snap.Services {
		if len(snap.Services[i].Owned) == 0 {
			missing[snap.Services[i].Name] = i
		}
	}
	if len(missing) == 0 {
		return
	}

	for _, w := range list.Items {
		inst := w.Metadata.Labels[labelInstance]
		if inst == "" {
			continue
		}
		idx, ok := missing[inst]
		if !ok {
			continue
		}
		snap.Services[idx].Owned = append(snap.Services[idx].Owned, Component{
			Kind:      w.Kind,
			Name:      w.Metadata.Name,
			Namespace: w.Metadata.Namespace,
		})
	}
}

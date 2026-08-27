package status

import "github.com/ntmggr/k8s-status/internal/kube"

// FillMissingVersions supplies an app version for services where ArgoCD did not
// populate status.summary.images, which on a large cluster was a quarter of them.
//
// It resolves them through each Application's own status.resources, which names the
// workloads it owns, rather than guessing from the service name: several services
// deploy into one shared namespace, so name matching resolves only a handful of them.
//
// This costs nothing extra. It reuses the workload list already fetched for the
// unmanaged view, so it only does anything when that feature is on.
func FillMissingVersions(snap *Snapshot, list *kube.WorkloadList) {
	if snap == nil || list == nil {
		return
	}

	type key struct{ kind, ns, name string }
	images := make(map[key]string, len(list.Items))
	for _, w := range list.Items {
		if img := firstImage(w); img != "" {
			images[key{w.Kind, w.Metadata.Namespace, w.Metadata.Name}] = img
		}
	}

	for i := range snap.Services {
		svc := &snap.Services[i]
		if svc.AppVersion != "" {
			continue
		}
		for _, r := range svc.Owned {
			switch r.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
			default:
				continue
			}
			img, ok := images[key{r.Kind, r.Namespace, r.Name}]
			if !ok {
				continue
			}
			parsed := parseImage(img)
			svc.AppVersion, svc.Image = parsed.Tag, parsed.Full
			break
		}
	}
}

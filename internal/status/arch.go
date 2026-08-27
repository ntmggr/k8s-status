package status

import (
	"sort"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// FillArch records which CPU architecture each service can run on, worked out from the
// nodes its workloads are allowed to land on rather than from an image or a name.
//
// Only a service pinned to one architecture gets a badge, and only if it is not already
// marked as accelerator-backed. Architecture is a CPU fact: every GPU node in a fleet
// tends to be x86, so badging a GPU service "amd64" says nothing about the service and
// invites reading it as something to do with the device. Most workloads are free to run
// on either architecture anyway, and labelling those would be noise.
//
// Reuses the node list and workload list already fetched, so it costs no extra request
// and no extra permission.
func FillArch(snap *Snapshot, list *kube.WorkloadList, nodes *kube.NodeList) {
	if snap == nil || list == nil || nodes == nil || len(nodes.Items) == 0 {
		return
	}

	type key struct{ kind, ns, name string }
	arches := make(map[key][]string, len(list.Items))
	for _, w := range list.Items {
		seen := map[string]bool{}
		for _, n := range nodes.Items {
			if !schedulable(w.Spec.Template.Spec, n) {
				continue
			}
			if a := n.Status.NodeInfo.Architecture; a != "" {
				seen[a] = true
			}
		}
		out := make([]string, 0, len(seen))
		for a := range seen {
			out = append(out, a)
		}
		sort.Strings(out)
		arches[key{w.Kind, w.Metadata.Namespace, w.Metadata.Name}] = out
	}

	byArch := map[string]int{}
	for i := range snap.Services {
		svc := &snap.Services[i]
		svc.Arch = "" // idempotent: the collector may decorate a cached snapshot twice
		if svc.GPU {
			continue
		}
		seen := map[string]bool{}
		for _, r := range svc.Owned {
			switch r.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
			default:
				continue
			}
			for _, a := range arches[key{r.Kind, r.Namespace, r.Name}] {
				seen[a] = true
			}
		}
		if len(seen) != 1 {
			continue
		}
		for a := range seen {
			svc.Arch = a
			byArch[a]++
		}
	}

	snap.ArchCounts = make([]ArchCount, 0, len(byArch))
	for a, c := range byArch {
		snap.ArchCounts = append(snap.ArchCounts, ArchCount{Arch: a, Count: c})
	}
	sort.Slice(snap.ArchCounts, func(i, j int) bool {
		if snap.ArchCounts[i].Count != snap.ArchCounts[j].Count {
			return snap.ArchCounts[i].Count > snap.ArchCounts[j].Count
		}
		return snap.ArchCounts[i].Arch < snap.ArchCounts[j].Arch
	})
}

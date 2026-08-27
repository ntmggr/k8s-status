package status

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// insufficientRe pulls the resource names out of a scheduler message such as
// "0/84 nodes are available: 1 Insufficient memory, 6 Insufficient cpu".
var insufficientRe = regexp.MustCompile(`Insufficient ([\w./-]+)`)

// owner ties a workload name back to the service that owns it.
type owner struct {
	name string
	idx  int
}

// Blocked describes a service the scheduler could not place. It is not a health state:
// the workload is fine, there is simply nowhere to run it.
type Blocked struct {
	// Resources the cluster ran out of, such as cpu, memory or nvidia.com/gpu.
	Resources []string
	// NoNodeMatched is set when every node was excluded by affinity or a taint rather
	// than by capacity. For a workload pinned to one nodegroup that means the group is
	// empty, which is the same problem wearing a different message.
	NoNodeMatched bool
	// Selector names what the workload insists on when nothing matched, taken from its
	// required node affinity. "No matching node" is true but not actionable; "no
	// gpu-pool/blue node" says which group is empty.
	Selector string
	// Pods is how many of the service's pods are stuck this way.
	Pods int
}

// Kind separates the shortages that need different people to fix them. Running out of
// accelerators is a capacity decision about expensive hardware; running out of cpu or
// memory is ordinary right-sizing; matching no node at all is usually a nodegroup that
// scaled to nothing.
func (b Blocked) Kind() string {
	for _, r := range b.Resources {
		if isAcceleratorResource(r) {
			return "accelerator"
		}
	}
	if len(b.Resources) > 0 {
		return "cpu"
	}
	return "placement"
}

// Reason is a short phrase for the row: what the cluster ran out of, or that nothing
// matched at all.
func (b Blocked) Reason() string {
	if len(b.Resources) > 0 {
		return "no " + strings.Join(b.Resources, ", ")
	}
	if b.NoNodeMatched {
		if b.Selector != "" {
			return "no " + b.Selector + " node"
		}
		return "no matching node"
	}
	return "unschedulable"
}

// FillPending marks services whose pods the scheduler has refused to place, and why.
//
// The verdict is the pod's own PodScheduled condition, read from pods still in Pending.
//
// FailedScheduling events were tried first, to avoid the wider permission that reading
// pods needs. They are wrong often enough to matter: an event outlives the problem it
// describes by up to an hour, so on one cluster every pod of a service was Running
// while stale events still reported a cpu shortage, and the page showed a service as
// blocked that was serving traffic. A condition on a pending pod cannot say that,
// because the pod stops being pending.
//
// A pod is matched back to its service through ownerReferences where possible, since a
// Deployment's pod is owned by a ReplicaSet named "<deployment>-<hash>". Name prefix is
// the fallback, longest match first, so "api" does not claim a pod belonging to
// "api-canary".
func FillPending(snap *Snapshot, pods *kube.PodList, workloads *kube.WorkloadList) {
	if snap == nil {
		return
	}
	if pods == nil || len(pods.Items) == 0 {
		for i := range snap.Services {
			snap.Services[i].Blocked = nil
		}
		snap.Summary.Blocked, snap.Summary.BlockedGPU = 0, 0
		snap.Summary.BlockedCPU, snap.Summary.BlockedPlacement = 0, 0
		return
	}

	// workload name -> service index, per namespace.
	byNS := map[string][]owner{}
	for i := range snap.Services {
		for _, r := range snap.Services[i].Owned {
			switch r.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
				byNS[r.Namespace] = append(byNS[r.Namespace], owner{r.Name, i})
			}
		}
	}

	// Same reason as FillGPU: a cached snapshot can be decorated more than once, so
	// anything this function owns is cleared before it is set.
	for i := range snap.Services {
		snap.Services[i].Blocked = nil
	}
	snap.Summary.Blocked, snap.Summary.BlockedGPU = 0, 0
	snap.Summary.BlockedCPU, snap.Summary.BlockedPlacement = 0, 0

	acc := map[int]*Blocked{}
	for _, pod := range pods.Items {
		msg, stuck := pod.Unschedulable()
		if !stuck {
			continue
		}
		bestIdx := ownerIndex(byNS[pod.Metadata.Namespace], pod)
		if bestIdx < 0 {
			continue
		}
		b := acc[bestIdx]
		if b == nil {
			b = &Blocked{}
			acc[bestIdx] = b
		}
		b.Pods++
		for _, m := range insufficientRe.FindAllStringSubmatch(msg, -1) {
			// A resource name may contain dots (nvidia.com/gpu), so they cannot be
			// excluded from the pattern; the sentence's own punctuation is trimmed
			// instead, or "cpu." and "cpu" would count as two different shortages.
			if r := strings.TrimRight(m[1], ".,;"); r != "" {
				b.Resources = appendUnique(b.Resources, r)
			}
		}
		if strings.Contains(msg, "didn't match Pod's node affinity/selector") ||
			strings.Contains(msg, "untolerated taint") {
			b.NoNodeMatched = true
		}
	}

	required := requiredSelectors(workloads)
	for idx, b := range acc {
		sort.Strings(b.Resources)
		if b.NoNodeMatched && len(b.Resources) == 0 {
			b.Selector = selectorFor(snap.Services[idx], required)
		}
		snap.Services[idx].Blocked = b
		snap.Summary.Blocked++
		switch b.Kind() {
		case "accelerator":
			snap.Summary.BlockedGPU++
		case "cpu":
			snap.Summary.BlockedCPU++
		default:
			snap.Summary.BlockedPlacement++
		}
	}
	// The rows were ordered before any of this was known, and a blocked row outranks
	// what its state alone would suggest.
	if len(acc) > 0 {
		sortServices(snap.Services)
	}
}

// ownerIndex resolves which service a pod belongs to. A Deployment's pod is owned by a
// ReplicaSet called "<deployment>-<hash>", so trimming the last segment names the
// workload exactly. StatefulSets and DaemonSets own their pods directly. Name prefix is
// the fallback for anything else, longest match first.
func ownerIndex(owners []owner, pod kube.Pod) int {
	for _, ref := range pod.Metadata.OwnerReferences {
		want := ref.Name
		if ref.Kind == "ReplicaSet" {
			if i := strings.LastIndex(want, "-"); i > 0 {
				want = want[:i]
			}
		}
		for _, o := range owners {
			if o.name == want {
				return o.idx
			}
		}
	}
	return prefixOwner(owners, pod.Metadata.Name)
}

func prefixOwner(owners []owner, podName string) int {
	best, bestIdx := "", -1
	for _, o := range owners {
		if strings.HasPrefix(podName, o.name+"-") && len(o.name) > len(best) {
			best, bestIdx = o.name, o.idx
		}
	}
	return bestIdx
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

// requiredSelectors reads what each workload insists on: the values of its required
// node affinity, which on a labelled fleet name the nodegroup it is pinned to.
func requiredSelectors(list *kube.WorkloadList) map[string]string {
	out := map[string]string{}
	if list == nil {
		return out
	}
	for _, w := range list.Items {
		ps := w.Spec.Template.Spec
		var vals []string
		for k, v := range ps.NodeSelector {
			_ = k
			vals = append(vals, v)
		}
		if ps.Affinity != nil && ps.Affinity.NodeAffinity != nil && ps.Affinity.NodeAffinity.Required != nil {
			for _, t := range ps.Affinity.NodeAffinity.Required.NodeSelectorTerms {
				for _, e := range t.MatchExpressions {
					if e.Operator == "In" {
						vals = append(vals, e.Values...)
					}
				}
			}
		}
		if len(vals) == 0 {
			continue
		}
		sort.Strings(vals)
		out[w.Kind+"/"+w.Metadata.Namespace+"/"+w.Metadata.Name] = strings.Join(vals, "/")
	}
	return out
}

func selectorFor(svc Service, required map[string]string) string {
	for _, r := range svc.Owned {
		if v, ok := required[r.Kind+"/"+r.Namespace+"/"+r.Name]; ok {
			return v
		}
	}
	return ""
}

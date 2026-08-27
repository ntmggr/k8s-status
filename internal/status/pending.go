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

// Blocked describes a service the scheduler could not place. It is not a health state:
// the workload is fine, there is simply nowhere to run it.
type Blocked struct {
	// Resources the cluster ran out of, such as cpu, memory or nvidia.com/gpu.
	Resources []string
	// NoNodeMatched is set when every node was excluded by affinity or a taint rather
	// than by capacity. For a workload pinned to one nodegroup that means the group is
	// empty, which is the same problem wearing a different message.
	NoNodeMatched bool
	// Pods is how many of the service's pods are stuck this way.
	Pods int
}

// Reason is a short phrase for the row: what the cluster ran out of, or that nothing
// matched at all.
func (b Blocked) Reason() string {
	if len(b.Resources) > 0 {
		return "no " + strings.Join(b.Resources, ", ")
	}
	if b.NoNodeMatched {
		return "no matching node"
	}
	return "unschedulable"
}

// FillPending marks services whose pods the scheduler has refused to place, and why.
//
// Events name a pod, not a workload, so the pod is matched back to a workload by name
// prefix within its namespace. The longest match wins: "tts-engine" and
// "tts-engine-daphne" both prefix a daphne pod, and only the longer one owns it.
func FillPending(snap *Snapshot, events *kube.EventList) {
	if snap == nil || events == nil || len(events.Items) == 0 {
		return
	}

	// workload name -> service index, per namespace.
	type owner struct {
		name string
		idx  int
	}
	byNS := map[string][]owner{}
	for i := range snap.Services {
		for _, r := range snap.Services[i].Owned {
			switch r.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
				byNS[r.Namespace] = append(byNS[r.Namespace], owner{r.Name, i})
			}
		}
	}

	acc := map[int]*Blocked{}
	for _, e := range events.Items {
		io := e.InvolvedObject
		if io.Kind != "Pod" {
			continue
		}
		ns := io.Namespace
		if ns == "" {
			ns = e.Metadata.Namespace
		}
		best, bestIdx := "", -1
		for _, o := range byNS[ns] {
			if strings.HasPrefix(io.Name, o.name+"-") && len(o.name) > len(best) {
				best, bestIdx = o.name, o.idx
			}
		}
		if bestIdx < 0 {
			continue
		}
		b := acc[bestIdx]
		if b == nil {
			b = &Blocked{}
			acc[bestIdx] = b
		}
		b.Pods++
		for _, m := range insufficientRe.FindAllStringSubmatch(e.Message, -1) {
			// A resource name may contain dots (nvidia.com/gpu), so they cannot be
			// excluded from the pattern; the sentence's own punctuation is trimmed
			// instead, or "cpu." and "cpu" would count as two different shortages.
			if r := strings.TrimRight(m[1], ".,;"); r != "" {
				b.Resources = appendUnique(b.Resources, r)
			}
		}
		if strings.Contains(e.Message, "didn't match Pod's node affinity/selector") ||
			strings.Contains(e.Message, "untolerated taint") {
			b.NoNodeMatched = true
		}
	}

	for idx, b := range acc {
		sort.Strings(b.Resources)
		snap.Services[idx].Blocked = b
		snap.Summary.Blocked++
	}
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

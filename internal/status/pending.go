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
// The verdict comes from the scheduler's own FailedScheduling events rather than from
// pod objects. Reading pods would need list access over every pod in the cluster, and a
// pod spec carries environment values that are routinely credentials; no amount of
// careful parsing narrows a grant that wide. An Event carries an object reference, a
// reason and a message.
//
// The usual objection to events is the one-hour TTL. It does not bite here: the
// scheduler retries a pending pod with backoff and refreshes the event each time, so a
// pod that is actually stuck keeps a current event. Measured on a live cluster, every
// unschedulable pod had an event under two minutes old.
//
// Events name a pod, not a workload, so it is matched back by name prefix within its
// namespace, longest match first: "tts-engine" does not claim a pod belonging to
// "tts-engine-daphne".
func FillPending(snap *Snapshot, events *kube.EventList) {
	if snap == nil {
		return
	}
	if events == nil || len(events.Items) == 0 {
		for i := range snap.Services {
			snap.Services[i].Blocked = nil
		}
		snap.Summary.Blocked = 0
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
	snap.Summary.Blocked = 0

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
		msg := e.Message
		bestIdx := prefixOwner(byNS[ns], io.Name)
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

	for idx, b := range acc {
		sort.Strings(b.Resources)
		snap.Services[idx].Blocked = b
		snap.Summary.Blocked++
	}
	// The rows were ordered before any of this was known, and a blocked row outranks
	// what its state alone would suggest.
	if len(acc) > 0 {
		sortServices(snap.Services)
	}
}

// prefixOwner resolves which service a pod belongs to by name, longest match first, so
// "tts-engine" does not claim a pod that belongs to "tts-engine-daphne".
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

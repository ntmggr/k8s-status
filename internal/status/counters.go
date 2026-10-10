package status

import "github.com/ntmggr/k8s-status/internal/kube"

// PodStats is a cluster-wide tally of running pods, from the same read AZ_SPREAD's
// zone/mesh join already uses. Every pod here is Running: Kubernetes only ever
// returns this list filtered server-side (see runningPodsPath), so this counts
// "running, and is it actually Ready" rather than every pod regardless of phase --
// a full unfiltered pod list is deliberately not something this project reads.
type PodStats struct {
	Total, Ready int
}

// NotReady is Total minus Ready: Running pods failing their own readiness probe.
func (s PodStats) NotReady() int { return s.Total - s.Ready }

// Percent is Ready as a percentage of Total, for the ring's own fill.
func (s PodStats) Percent() int { return roundPercent(s.Ready, s.Total) }

// BuildPodStats sums Pod.Ready() across the running-pods read. Nil in, zero value
// out: the caller only assigns Snapshot.Pods when that read actually succeeded.
func BuildPodStats(pods *kube.PodList) PodStats {
	if pods == nil {
		return PodStats{}
	}
	s := PodStats{}
	for _, p := range pods.Items {
		s.Total++
		if p.Ready() {
			s.Ready++
		}
	}
	return s
}

// WorkloadStats is a cluster-wide tally of every Deployment/StatefulSet/DaemonSet the
// cluster runs, GitOps-managed or not -- the same list the "not managed by GitOps"
// section already reads. Ready/Desired sum each workload's own counters via the same
// readiness() helper that section and FillReadiness both already use.
type WorkloadStats struct {
	Total, Ready, Desired int
}

// Percent is Ready as a percentage of Desired, for the ring's own fill.
func (s WorkloadStats) Percent() int { return roundPercent(s.Ready, s.Desired) }

// BuildWorkloadStats sums readiness() across the cluster-wide workload read. Nil in,
// zero value out, same convention as BuildPodStats.
func BuildWorkloadStats(list *kube.WorkloadList) WorkloadStats {
	if list == nil {
		return WorkloadStats{}
	}
	s := WorkloadStats{}
	for _, w := range list.Items {
		s.Total++
		ready, desired := readiness(w)
		s.Ready += ready
		s.Desired += desired
	}
	return s
}

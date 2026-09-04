package status

import "github.com/ntmggr/k8s-status/internal/kube"

// JobInfo is one Job or CronJob owned by a service. Informational only: see the
// Jobs field comment on Service for why this never affects Health/State.
type JobInfo struct {
	// Kind is "Job" or "CronJob".
	Kind string
	Name string

	// Job fields. Zero value for a CronJob.
	Active         int
	Succeeded      int
	Failed         int
	CompletionTime string

	// CronJob fields. Zero value for a Job.
	Schedule           string
	Suspended          bool
	LastScheduleTime   string
	LastSuccessfulTime string
	ActiveRuns         int
}

// State is a short label for a Job's own run. Empty for a CronJob: rather than guess
// at its last run's outcome from LastScheduleTime/LastSuccessfulTime, which is not a
// documented contract, its raw fields are shown instead and the viewer draws the
// conclusion.
func (j JobInfo) State() string {
	switch {
	case j.Kind != "Job":
		return ""
	case j.Active > 0:
		return "Active"
	case j.Failed > 0:
		return "Failed"
	case j.Succeeded > 0:
		return "Succeeded"
	default:
		return ""
	}
}

// FillJobs matches each service's owned Job/CronJob components (from ArgoCD's own
// resource tree, via ownedWorkloads) to the actual objects, by name and namespace.
// No owner-reference indirection needed here, unlike matching a running Pod to a
// Deployment: ArgoCD already names the Job or CronJob itself, not an intermediate
// controller it created.
func FillJobs(snap *Snapshot, jobs *kube.JobList, cronJobs *kube.CronJobList) {
	if snap == nil {
		return
	}
	// Idempotent against a cached snapshot being decorated more than once, the same
	// reason FillZones resets instead of appending.
	for i := range snap.Services {
		snap.Services[i].Jobs = nil
	}

	type key struct{ ns, name string }
	byJob := map[key]kube.Job{}
	if jobs != nil {
		for _, j := range jobs.Items {
			byJob[key{j.Metadata.Namespace, j.Metadata.Name}] = j
		}
	}
	byCron := map[key]kube.CronJob{}
	if cronJobs != nil {
		for _, c := range cronJobs.Items {
			byCron[key{c.Metadata.Namespace, c.Metadata.Name}] = c
		}
	}

	for i := range snap.Services {
		for _, o := range snap.Services[i].Owned {
			switch o.Kind {
			case "Job":
				j, ok := byJob[key{o.Namespace, o.Name}]
				if !ok {
					continue
				}
				snap.Services[i].Jobs = append(snap.Services[i].Jobs, JobInfo{
					Kind:           "Job",
					Name:           o.Name,
					Active:         j.Status.Active,
					Succeeded:      j.Status.Succeeded,
					Failed:         j.Status.Failed,
					CompletionTime: j.Status.CompletionTime,
				})
			case "CronJob":
				cj, ok := byCron[key{o.Namespace, o.Name}]
				if !ok {
					continue
				}
				snap.Services[i].Jobs = append(snap.Services[i].Jobs, JobInfo{
					Kind:               "CronJob",
					Name:               o.Name,
					Schedule:           cj.Spec.Schedule,
					Suspended:          cj.Spec.Suspend != nil && *cj.Spec.Suspend,
					LastScheduleTime:   cj.Status.LastScheduleTime,
					LastSuccessfulTime: cj.Status.LastSuccessfulTime,
					ActiveRuns:         len(cj.Status.Active),
				})
			}
		}
	}
}

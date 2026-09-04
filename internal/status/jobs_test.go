package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func TestJobInfoState(t *testing.T) {
	cases := []struct {
		name string
		j    JobInfo
		want string
	}{
		{"active wins over succeeded/failed counts", JobInfo{Kind: "Job", Active: 1, Succeeded: 2, Failed: 3}, "Active"},
		{"failed beats succeeded", JobInfo{Kind: "Job", Failed: 1, Succeeded: 2}, "Failed"},
		{"succeeded", JobInfo{Kind: "Job", Succeeded: 1}, "Succeeded"},
		{"no counts yet", JobInfo{Kind: "Job"}, ""},
		{"cronjob never gets a derived state", JobInfo{Kind: "CronJob", Succeeded: 5}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.j.State(); got != c.want {
				t.Errorf("State() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFillJobsMatchesOwnedByNameAndNamespace(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "billing", Owned: []Component{
		{Kind: "Job", Name: "billing-migrate", Namespace: "billing"},
		{Kind: "CronJob", Name: "billing-nightly", Namespace: "billing"},
		{Kind: "Deployment", Name: "billing-api", Namespace: "billing"}, // ignored, not a job kind
	}})

	jobs := &kube.JobList{Items: []kube.Job{
		{Metadata: kube.WorkloadMetadata{Name: "billing-migrate", Namespace: "billing"},
			Status: kube.JobStatus{Succeeded: 1, CompletionTime: "2026-01-01T00:00:00Z"}},
		{Metadata: kube.WorkloadMetadata{Name: "unrelated-job", Namespace: "other"},
			Status: kube.JobStatus{Succeeded: 1}},
	}}
	cronJobs := &kube.CronJobList{Items: []kube.CronJob{
		{Metadata: kube.WorkloadMetadata{Name: "billing-nightly", Namespace: "billing"},
			Spec:   kube.CronJobSpec{Schedule: "0 0 * * *"},
			Status: kube.CronJobStatus{LastScheduleTime: "2026-01-01T00:00:00Z", LastSuccessfulTime: "2026-01-01T00:00:01Z"}},
	}}

	FillJobs(snap, jobs, cronJobs)

	got := snap.Services[0].Jobs
	if len(got) != 2 {
		t.Fatalf("Jobs = %+v, want 2 (the unrelated job and the ignored Deployment must not appear)", got)
	}
	var job, cron *JobInfo
	for i := range got {
		switch got[i].Kind {
		case "Job":
			job = &got[i]
		case "CronJob":
			cron = &got[i]
		}
	}
	if job == nil || job.Name != "billing-migrate" || job.Succeeded != 1 || job.State() != "Succeeded" {
		t.Errorf("job = %+v, want billing-migrate/Succeeded", job)
	}
	if cron == nil || cron.Name != "billing-nightly" || cron.Schedule != "0 0 * * *" {
		t.Errorf("cron = %+v, want billing-nightly on schedule 0 0 * * *", cron)
	}
}

func TestFillJobsIgnoresUnmatchedOwnedComponent(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "billing", Owned: []Component{
		{Kind: "Job", Name: "gone-now", Namespace: "billing"},
	}})

	FillJobs(snap, &kube.JobList{}, &kube.CronJobList{})

	if got := snap.Services[0].Jobs; len(got) != 0 {
		t.Errorf("Jobs = %+v, want empty when the owned Job no longer exists", got)
	}
}

func TestFillJobsResetsOnRepeatedCalls(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "billing", Owned: []Component{
		{Kind: "Job", Name: "migrate", Namespace: "billing"},
	}})
	jobs := &kube.JobList{Items: []kube.Job{
		{Metadata: kube.WorkloadMetadata{Name: "migrate", Namespace: "billing"}, Status: kube.JobStatus{Succeeded: 1}},
	}}

	for i := 0; i < 3; i++ {
		FillJobs(snap, jobs, &kube.CronJobList{})
	}
	if got := len(snap.Services[0].Jobs); got != 1 {
		t.Errorf("Jobs count after 3 calls = %d, want 1 (must not accumulate)", got)
	}
}

func TestFillJobsNilSnapshotIsNoop(t *testing.T) {
	FillJobs(nil, &kube.JobList{}, &kube.CronJobList{})
}

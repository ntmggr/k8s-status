package status

import (
	"context"
	"testing"
	"time"

	"github.com/ntmggr/k8s-status/internal/kube"
)

type fakeJobLister struct {
	called   int
	jobs     *kube.JobList
	cronJobs *kube.CronJobList
	err      error
}

func (f *fakeJobLister) ListJobs(context.Context) (*kube.JobList, *kube.CronJobList, error) {
	f.called++
	return f.jobs, f.cronJobs, f.err
}

// The default deployment holds no ClusterRole, so a collector without WithJobs must
// never reach for the cluster-wide batch APIs.
func TestJobsNotFetchedWhenDisabled(t *testing.T) {
	jobs := &fakeJobLister{jobs: &kube.JobList{}, cronJobs: &kube.CronJobList{}}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	if jobs.called != 0 {
		t.Errorf("jobs API called %d times, want 0", jobs.called)
	}
}

func TestJobsFetchedOncePerTTL(t *testing.T) {
	jobs := &fakeJobLister{jobs: &kube.JobList{}, cronJobs: &kube.CronJobList{}}
	c, clock := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithJobs(jobs)

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if jobs.called != 1 {
		t.Errorf("jobs fetches within TTL = %d, want 1", jobs.called)
	}

	clock.advance(16 * time.Second)
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("refresh get: %v", err)
	}
	if jobs.called != 2 {
		t.Errorf("jobs fetches after TTL = %d, want 2", jobs.called)
	}
}

// A transient error must keep whatever was last read, the same as fetchWorkloads: an
// informational section going blank on one failed poll is worse than staying stale.
func TestJobsErrorKeepsLastGoodRead(t *testing.T) {
	good := &kube.JobList{Items: []kube.Job{{Metadata: kube.WorkloadMetadata{Name: "migrate", Namespace: "ns"}}}}
	jobs := &fakeJobLister{jobs: good, cronJobs: &kube.CronJobList{}}
	c, clock := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithJobs(jobs)

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.jobList != good {
		t.Fatalf("jobList = %+v, want the first good read", c.jobList)
	}

	clock.advance(16 * time.Second)
	jobs.err = context.DeadlineExceeded
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("get after failure: %v", err)
	}
	if c.jobList != good {
		t.Errorf("jobList = %+v, want the stale-but-good read kept on failure", c.jobList)
	}
}

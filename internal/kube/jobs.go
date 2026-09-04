package kube

import (
	"context"
	"errors"
	"sync"
)

// Cluster-wide collection endpoints, the same shape as workloads.go's.
const (
	jobsPath     = "/apis/batch/v1/jobs"
	cronJobsPath = "/apis/batch/v1/cronjobs"
)

const (
	KindJob     = "Job"
	KindCronJob = "CronJob"
)

type JobList struct {
	Items []Job `json:"items"`
}

// Job decodes only the fields the Jobs section needs.
type Job struct {
	Metadata WorkloadMetadata `json:"metadata"`
	Spec     JobSpec          `json:"spec"`
	Status   JobStatus        `json:"status"`
}

type JobSpec struct {
	// Completions is a pointer so "absent" (Kubernetes defaults it to 1) is
	// distinguishable from an explicit value, the same reason WorkloadSpec.Replicas
	// and CronJobSpec.Suspend are pointers too.
	Completions *int `json:"completions"`
}

type JobStatus struct {
	Active         int    `json:"active"`
	Succeeded      int    `json:"succeeded"`
	Failed         int    `json:"failed"`
	CompletionTime string `json:"completionTime"`
}

type CronJobList struct {
	Items []CronJob `json:"items"`
}

type CronJob struct {
	Metadata WorkloadMetadata `json:"metadata"`
	Spec     CronJobSpec      `json:"spec"`
	Status   CronJobStatus    `json:"status"`
}

type CronJobSpec struct {
	Schedule string `json:"schedule"`
	// Suspend is a pointer so "absent" (defaults to false) is distinguishable from an
	// explicit false, the same reason WorkloadSpec.Replicas is one.
	Suspend *bool `json:"suspend"`
}

type CronJobStatus struct {
	LastScheduleTime   string `json:"lastScheduleTime"`
	LastSuccessfulTime string `json:"lastSuccessfulTime"`
	// Active is decoded for its length alone: how many runs are in flight right now.
	Active []struct{} `json:"active"`
}

// ListJobs returns every Job and CronJob in the cluster. Cluster-wide reads, so they
// need a ClusterRole granting get/list on jobs and cronjobs in the batch group.
//
// The two kinds are fetched concurrently under one timeout, same partial-failure
// tolerance as ListWorkloads: one failing does not discard the other.
func (c *Client) ListJobs(ctx context.Context) (*JobList, *CronJobList, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	var (
		jobs             JobList
		cronJobs         CronJobList
		jobsErr, cronErr error
		wg               sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		jobsErr = c.GetJSON(ctx, FromCache(jobsPath), "job list", &jobs)
	}()
	go func() {
		defer wg.Done()
		cronErr = c.GetJSON(ctx, FromCache(cronJobsPath), "cronjob list", &cronJobs)
	}()
	wg.Wait()

	return &jobs, &cronJobs, errors.Join(jobsErr, cronErr)
}

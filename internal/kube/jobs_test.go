package kube

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestListJobsFetchesBothKinds(t *testing.T) {
	var seen atomic.Int64
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		switch r.URL.Path {
		case jobsPath:
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"migrate","namespace":"ns"},
			  "status":{"succeeded":1,"completionTime":"2026-01-01T00:00:00Z"}}]}`))
		case cronJobsPath:
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"nightly","namespace":"ns"},
			  "spec":{"schedule":"0 0 * * *"},
			  "status":{"lastScheduleTime":"2026-01-01T00:00:00Z","lastSuccessfulTime":"2026-01-01T00:00:01Z"}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))

	jobs, cronJobs, err := c.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if seen.Load() != 2 {
		t.Errorf("api calls = %d, want 2", seen.Load())
	}
	if len(jobs.Items) != 1 || jobs.Items[0].Metadata.Name != "migrate" || jobs.Items[0].Status.Succeeded != 1 {
		t.Errorf("jobs = %+v, want one item named migrate with succeeded=1", jobs.Items)
	}
	if len(cronJobs.Items) != 1 || cronJobs.Items[0].Metadata.Name != "nightly" || cronJobs.Items[0].Spec.Schedule != "0 0 * * *" {
		t.Errorf("cronJobs = %+v, want one item named nightly with that schedule", cronJobs.Items)
	}
}

func TestListJobsOneFailingDoesNotDiscardTheOther(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case jobsPath:
			w.WriteHeader(http.StatusForbidden)
		case cronJobsPath:
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"nightly","namespace":"ns"}}]}`))
		}
	}))

	jobs, cronJobs, err := c.ListJobs(context.Background())
	if err == nil {
		t.Fatal("want a joined error when one kind fails")
	}
	if jobs == nil || len(jobs.Items) != 0 {
		t.Errorf("jobs = %+v, want an empty (not nil) list", jobs)
	}
	if cronJobs == nil || len(cronJobs.Items) != 1 {
		t.Errorf("cronJobs = %+v, want the one item that did succeed", cronJobs)
	}
}

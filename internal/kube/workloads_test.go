package kube

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

func TestListWorkloadsFetchesAllThreeKindsAndTagsThem(t *testing.T) {
	var seen atomic.Int64
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		switch r.URL.Path {
		case deploymentsPath:
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"d1","namespace":"ns"},
			  "spec":{"template":{"spec":{"containers":[{"image":"repo/d:1.2.3"}]}}},
			  "status":{"replicas":2,"readyReplicas":2}}]}`))
		case statefulSetsPath:
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"s1","namespace":"ns"},
			  "status":{"replicas":3,"readyReplicas":1}}]}`))
		case daemonSetsPath:
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"x1","namespace":"ns"},
			  "status":{"desiredNumberScheduled":82,"numberReady":81}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))

	list, err := c.ListWorkloads(context.Background())
	if err != nil {
		t.Fatalf("ListWorkloads: %v", err)
	}
	if seen.Load() != 3 {
		t.Errorf("api calls = %d, want 3", seen.Load())
	}
	if len(list.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(list.Items))
	}

	byKind := map[string]Workload{}
	for _, w := range list.Items {
		byKind[w.Kind] = w
	}
	for _, k := range []string{KindDeployment, KindStatefulSet, KindDaemonSet} {
		if _, ok := byKind[k]; !ok {
			t.Fatalf("no item tagged %s: %+v", k, list.Items)
		}
	}
	if got := byKind[KindDeployment].Spec.Template.Spec.Containers[0].Image; got != "repo/d:1.2.3" {
		t.Errorf("deployment image = %q", got)
	}
	if got := byKind[KindDaemonSet].Status.NumberReady; got != 81 {
		t.Errorf("daemonset numberReady = %d, want 81", got)
	}
}

// One denied kind must not throw away the two that answered.
func TestListWorkloadsPartialFailureKeepsWhatItGot(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == statefulSetsPath {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("statefulsets is forbidden"))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"a","namespace":"ns"}}]}`))
	}))

	list, err := c.ListWorkloads(context.Background())
	if err == nil {
		t.Fatal("want the failing kind reported")
	}
	if len(list.Items) != 2 {
		t.Errorf("items = %d, want the 2 kinds that succeeded", len(list.Items))
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("error = %v, want a 403 *StatusError", err)
	}
}

func TestListWorkloadsAllDenied(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))

	list, err := c.ListWorkloads(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if len(list.Items) != 0 {
		t.Errorf("items = %d, want 0", len(list.Items))
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a *StatusError", err)
	}
}

func TestListWorkloadsUsesClusterWidePaths(t *testing.T) {
	var mu = make(chan string, 3)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu <- r.URL.Path
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	if _, err := c.ListWorkloads(context.Background()); err != nil {
		t.Fatalf("ListWorkloads: %v", err)
	}
	close(mu)

	var paths []string
	for p := range mu {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	want := []string{daemonSetsPath, deploymentsPath, statefulSetsPath}
	sort.Strings(want)
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths = %v, want %v", paths, want)
			break
		}
	}
	for _, p := range paths {
		if strings.Contains(p, "namespaces") {
			t.Errorf("path %q is namespaced; the cluster-wide form is required", p)
		}
	}
}

func TestListWorkloadsMalformedJSON(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == deploymentsPath {
			_, _ = w.Write([]byte(`{"items": [ {"metadata": `))
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))

	if _, err := c.ListWorkloads(context.Background()); err == nil {
		t.Fatal("want decode error")
	} else if !strings.Contains(err.Error(), "decode deployment list") {
		t.Errorf("error = %v", err)
	}
}

package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// pagedHandler replays one canned JSON response per call, in order, and records the
// full URL of every request it served.
type pagedHandler struct {
	mu       sync.Mutex
	bodies   []string
	requests []string
}

func (h *pagedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	n := len(h.requests)
	h.requests = append(h.requests, r.URL.String())
	h.mu.Unlock()

	if n >= len(h.bodies) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("no more canned pages"))
		return
	}
	_, _ = w.Write([]byte(h.bodies[n]))
}

func (h *pagedHandler) seenRequests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.requests...)
}

func TestListRunningPodsFollowsContinueToken(t *testing.T) {
	h := &pagedHandler{bodies: []string{
		`{"metadata":{"continue":"tok-1"},"items":[{"metadata":{"name":"a"},"status":{"phase":"Running","hostIP":"10.0.1.1"}}]}`,
		`{"metadata":{"continue":""},"items":[{"metadata":{"name":"b"},"status":{"phase":"Running","hostIP":"10.0.1.2"}}]}`,
	}}
	c := newTestClient(t, h)

	list, err := c.ListRunningPods(context.Background())
	if err != nil {
		t.Fatalf("ListRunningPods: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("items = %d, want 2 (both pages)", len(list.Items))
	}
	if list.Items[0].Metadata.Name != "a" || list.Items[1].Metadata.Name != "b" {
		t.Errorf("items = %+v, want a then b in page order", list.Items)
	}

	reqs := h.seenRequests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if strings.Contains(reqs[0], "continue=") {
		t.Errorf("first request should carry no continue token: %s", reqs[0])
	}
	if !strings.Contains(reqs[1], "continue=tok-1") {
		t.Errorf("second request should carry the first page's continue token: %s", reqs[1])
	}
}

// A page can legitimately come back with zero items while still carrying a non-empty
// continue token, because the fieldSelector is applied server-side after paging.
// Stopping on item count instead of the token would under-report silently.
func TestListRunningPodsZeroItemPageWithContinueIsNotEndOfList(t *testing.T) {
	h := &pagedHandler{bodies: []string{
		`{"metadata":{"continue":"tok-1"},"items":[]}`,
		`{"metadata":{"continue":""},"items":[{"metadata":{"name":"only"},"status":{"phase":"Running"}}]}`,
	}}
	c := newTestClient(t, h)

	list, err := c.ListRunningPods(context.Background())
	if err != nil {
		t.Fatalf("ListRunningPods: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1: a zero-item page must not stop pagination early", len(list.Items))
	}
	if len(h.seenRequests()) != 2 {
		t.Fatalf("requests = %d, want 2: the second page must still be fetched", len(h.seenRequests()))
	}
}

func TestListRunningPodsPageCapTripsCleanly(t *testing.T) {
	var bodies []string
	for i := 0; i < runningPodsMaxPages+1; i++ {
		bodies = append(bodies, fmt.Sprintf(`{"metadata":{"continue":"tok-%d"},"items":[]}`, i))
	}
	h := &pagedHandler{bodies: bodies}
	c := newTestClient(t, h)

	_, err := c.ListRunningPods(context.Background())
	if err == nil {
		t.Fatal("want an error once the page cap is hit")
	}
	if !errors.Is(err, ErrPodListTooLarge) {
		t.Fatalf("err = %v, want ErrPodListTooLarge (checkable via errors.Is)", err)
	}
	if got := len(h.seenRequests()); got != runningPodsMaxPages {
		t.Errorf("requests = %d, want exactly %d (the cap, not one more)", got, runningPodsMaxPages)
	}
}

// FromCache's resourceVersion=0 asks the API server to serve from its watch cache,
// which is incompatible with limit/continue pagination. ListRunningPods must never
// send it, on any page.
func TestListRunningPodsNeverUsesResourceVersionZero(t *testing.T) {
	h := &pagedHandler{bodies: []string{
		`{"metadata":{"continue":"tok-1"},"items":[]}`,
		`{"metadata":{"continue":""},"items":[]}`,
	}}
	c := newTestClient(t, h)

	if _, err := c.ListRunningPods(context.Background()); err != nil {
		t.Fatalf("ListRunningPods: %v", err)
	}
	for _, req := range h.seenRequests() {
		if strings.Contains(req, "resourceVersion") {
			t.Errorf("request must not carry resourceVersion: %s", req)
		}
	}
}

func TestListRunningPodsUsesThePageLimit(t *testing.T) {
	h := &pagedHandler{bodies: []string{`{"metadata":{"continue":""},"items":[]}`}}
	c := newTestClient(t, h)

	if _, err := c.ListRunningPods(context.Background()); err != nil {
		t.Fatalf("ListRunningPods: %v", err)
	}
	reqs := h.seenRequests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], "limit="+strconv.Itoa(runningPodsPageLimit)) {
		t.Errorf("requests = %v, want a limit=%d param", reqs, runningPodsPageLimit)
	}
}

func TestListRunningPodsDecodesHostIP(t *testing.T) {
	h := &pagedHandler{bodies: []string{
		`{"metadata":{"continue":""},"items":[{"metadata":{"name":"a","namespace":"ml"},"status":{"phase":"Running","hostIP":"10.0.1.5"}}]}`,
	}}
	c := newTestClient(t, h)

	list, err := c.ListRunningPods(context.Background())
	if err != nil {
		t.Fatalf("ListRunningPods: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Status.HostIP != "10.0.1.5" {
		t.Errorf("items = %+v, want hostIP 10.0.1.5", list.Items)
	}
}

func TestListRunningPodsDecodesContainerStatuses(t *testing.T) {
	h := &pagedHandler{bodies: []string{
		`{"metadata":{"continue":""},"items":[{"metadata":{"name":"a"},"status":{"phase":"Running","containerStatuses":[{"name":"app"},{"name":"istio-proxy"}]}}]}`,
	}}
	c := newTestClient(t, h)

	list, err := c.ListRunningPods(context.Background())
	if err != nil {
		t.Fatalf("ListRunningPods: %v", err)
	}
	if len(list.Items) != 1 || !list.Items[0].IsIstioInjected() {
		t.Errorf("items = %+v, want IsIstioInjected true", list.Items)
	}
}

// TestListRunningPodsDecodesNativeSidecarInitContainerStatuses covers Kubernetes
// 1.29+'s native sidecar containers, which is how Istio's own injector actually runs
// istio-proxy on a current cluster: as an init container with restartPolicy: Always,
// reported under initContainerStatuses rather than containerStatuses. Checking only
// the latter silently reported every real, correctly-injected pod on such a cluster
// as "not in mesh" -- caught by checking real cluster data, not a synthetic guess.
func TestListRunningPodsDecodesNativeSidecarInitContainerStatuses(t *testing.T) {
	h := &pagedHandler{bodies: []string{
		`{"metadata":{"continue":""},"items":[{"metadata":{"name":"a"},"status":{"phase":"Running","containerStatuses":[{"name":"app"}],"initContainerStatuses":[{"name":"istio-init"},{"name":"istio-proxy"}]}}]}`,
	}}
	c := newTestClient(t, h)

	list, err := c.ListRunningPods(context.Background())
	if err != nil {
		t.Fatalf("ListRunningPods: %v", err)
	}
	if len(list.Items) != 1 || !list.Items[0].IsIstioInjected() {
		t.Errorf("items = %+v, want IsIstioInjected true", list.Items)
	}
}

func TestIsIstioInjected(t *testing.T) {
	cases := []struct {
		name string
		pod  Pod
		want bool
	}{
		{"no containers", Pod{}, false},
		{"app only", Pod{Status: PodStatus{ContainerStatuses: []ContainerStatus{{Name: "app"}}}}, false},
		{"app and sidecar", Pod{Status: PodStatus{ContainerStatuses: []ContainerStatus{{Name: "app"}, {Name: "istio-proxy"}}}}, true},
		{"sidecar only", Pod{Status: PodStatus{ContainerStatuses: []ContainerStatus{{Name: "istio-proxy"}}}}, true},
		{
			name: "native sidecar as init container, not a regular one",
			pod: Pod{Status: PodStatus{
				ContainerStatuses:     []ContainerStatus{{Name: "app"}},
				InitContainerStatuses: []ContainerStatus{{Name: "istio-init"}, {Name: "istio-proxy"}},
			}},
			want: true,
		},
		{
			name: "istio-init alone is not the proxy itself",
			pod: Pod{Status: PodStatus{
				ContainerStatuses:     []ContainerStatus{{Name: "app"}},
				InitContainerStatuses: []ContainerStatus{{Name: "istio-init"}},
			}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pod.IsIstioInjected(); got != tc.want {
				t.Errorf("IsIstioInjected() = %v, want %v", got, tc.want)
			}
		})
	}
}

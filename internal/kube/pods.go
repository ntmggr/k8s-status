package kube

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// pendingPodsPath asks only for pods the scheduler has not placed. The filter is applied
// server-side, so a large cluster returns a handful of objects rather than thousands.
const pendingPodsPath = "/api/v1/pods?fieldSelector=status.phase%3DPending"

// runningPodsPath asks only for pods the scheduler has placed and that are currently
// running, for the AZ-spread section. Unlike pendingPodsPath this can return every
// running pod in the cluster, so ListRunningPods paginates it.
const runningPodsPath = "/api/v1/pods?fieldSelector=status.phase%3DRunning"

const (
	// runningPodsPageLimit bounds one page's response size well under maxBodyBytes.
	runningPodsPageLimit = 500
	// runningPodsMaxPages caps a single ListRunningPods call at 20,000 pods. A cluster
	// past that needs a narrower question than "every running pod", and ErrPodListTooLarge
	// says so rather than silently returning a truncated answer.
	runningPodsMaxPages = 40
)

// ErrPodListTooLarge is returned by ListRunningPods when the page cap is hit before the
// server ran out of pages. Callers should turn this into a "too large, section
// disabled" state, not a generic read failure.
var ErrPodListTooLarge = errors.New("running pod list exceeded the page cap")

type PodList struct {
	Items []Pod `json:"items"`
	// Metadata carries the continue token used to page through the running pod list.
	// Every other caller of PodList (ListPendingPods) leaves it empty and ignores it.
	Metadata struct {
		Continue string `json:"continue"`
	} `json:"metadata"`
}

// Pod deliberately decodes no spec. Reading pods is the widest permission this project
// takes, and a pod spec carries environment values that are routinely credentials.
// Nothing here can hold one, so a bug or a stray log line cannot leak what was never
// parsed. The grant is still the real control; this only limits the blast radius.
type Pod struct {
	Metadata PodMetadata `json:"metadata"`
	Status   PodStatus   `json:"status"`
}

type PodMetadata struct {
	Name            string           `json:"name"`
	Namespace       string           `json:"namespace"`
	OwnerReferences []OwnerReference `json:"ownerReferences"`
}

type PodStatus struct {
	Phase      string         `json:"phase"`
	Conditions []PodCondition `json:"conditions"`
	// HostIP is read instead of spec.nodeName so this file still decodes no spec at
	// all: joined against a node's own addresses, it says which node a pod landed on.
	HostIP string `json:"hostIP"`
	// ContainerStatuses is read instead of spec.containers so this file still decodes
	// no spec at all: a container's name is not a credential-bearing field the way
	// spec.containers[].env is, and joined against istioProxyContainer it says whether
	// this pod actually has the Istio sidecar injected.
	ContainerStatuses []ContainerStatus `json:"containerStatuses"`
}

type ContainerStatus struct {
	Name string `json:"name"`
}

// istioProxyContainer is the name Istio's sidecar injector always uses.
const istioProxyContainer = "istio-proxy"

// IsIstioInjected reports whether this pod actually has the Istio sidecar running,
// observed from its own container statuses rather than any injection annotation or
// namespace label, which only say a sidecar was requested.
func (p Pod) IsIstioInjected() bool {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == istioProxyContainer {
			return true
		}
	}
	return false
}

type PodCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Unschedulable returns the scheduler's verdict on this pod, and whether it gave one.
//
// This is the pod's own current state, which is why it is used instead of a
// FailedScheduling event. An event outlives the problem it describes: on one cluster
// every pod of a service was Running while hour-old events still said the cluster was
// out of cpu, and the page reported a shortage that had already been resolved.
func (p Pod) Unschedulable() (string, bool) {
	for _, c := range p.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" && c.Reason == "Unschedulable" {
			return c.Message, true
		}
	}
	return "", false
}

// ListPendingPods returns pods stuck in Pending.
func (c *Client) ListPendingPods(ctx context.Context) (*PodList, error) {
	var list PodList
	if err := c.GetJSON(ctx, FromCache(pendingPodsPath), "pod list", &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// ListRunningPods returns every Running pod in the cluster, for the AZ-spread section.
//
// This deliberately does not use FromCache. FromCache appends resourceVersion=0, which
// asks the API server to answer from its watch cache instead of a quorum read; that is
// incompatible with limit/continue pagination, which either errors or silently ignores
// one of the two. This is the one list call in the whole project that skips the
// FromCache convention, in exchange for a real quorum read on a call that has to
// paginate anyway. Do not "fix" this to use FromCache.
//
// Pagination follows metadata.continue rather than stopping once a page comes back
// with fewer than limit items: the fieldSelector is applied server-side after paging,
// so a page can legitimately return zero items while still carrying a non-empty
// continue token. Stopping on item count would under-report silently.
//
// A cluster whose running pod count exceeds runningPodsMaxPages*runningPodsPageLimit
// returns ErrPodListTooLarge instead of a truncated list, so the caller can disable the
// section rather than show a number that is quietly wrong.
func (c *Client) ListRunningPods(ctx context.Context) (*PodList, error) {
	var out PodList
	cont := ""
	for page := 0; page < runningPodsMaxPages; page++ {
		path := fmt.Sprintf("%s&limit=%d", runningPodsPath, runningPodsPageLimit)
		if cont != "" {
			path += "&continue=" + url.QueryEscape(cont)
		}
		var list PodList
		if err := c.GetJSON(ctx, path, "pod list", &list); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, list.Items...)
		if list.Metadata.Continue == "" {
			return &out, nil
		}
		cont = list.Metadata.Continue
	}
	return nil, ErrPodListTooLarge
}

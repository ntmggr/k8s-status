package kube

import "context"

// pendingPodsPath asks only for pods the scheduler has not placed. The filter is applied
// server-side, so a large cluster returns a handful of objects rather than thousands.
const pendingPodsPath = "/api/v1/pods?fieldSelector=status.phase%3DPending"

type PodList struct {
	Items []Pod `json:"items"`
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

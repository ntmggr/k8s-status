package kube

import (
	"context"
	"errors"
	"sync"
)

// Cluster-wide collection endpoints: the same lists the namespaced forms serve, without
// a namespace segment, so one call per kind covers every namespace.
const (
	deploymentsPath  = "/apis/apps/v1/deployments"
	statefulSetsPath = "/apis/apps/v1/statefulsets"
	daemonSetsPath   = "/apis/apps/v1/daemonsets"
)

const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
)

type WorkloadList struct {
	Items []Workload `json:"items"`
}

// Workload decodes only the fields the unmanaged section needs. The three kinds share
// one shape because the differing status fields do not collide.
type Workload struct {
	// Kind is filled in by the client: a collection response carries the kind once on
	// the list, not on each item.
	Kind     string           `json:"-"`
	Metadata WorkloadMetadata `json:"metadata"`
	Spec     WorkloadSpec     `json:"spec"`
	Status   WorkloadStatus   `json:"status"`
}

type WorkloadMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	// OwnerReferences is decoded for its length alone: an owned workload was created by
	// a controller in the cluster, not installed by a human, a chart or a pipeline.
	OwnerReferences []OwnerReference `json:"ownerReferences"`
}

type OwnerReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type WorkloadSpec struct {
	Template PodTemplate `json:"template"`
}

type PodTemplate struct {
	Spec PodSpec `json:"spec"`
}

type PodSpec struct {
	Containers []Container `json:"containers"`

	// NodeSelector and Affinity say which nodes this workload may run on. Some
	// workloads get a whole GPU node to themselves and talk to the device directly
	// instead of requesting nvidia.com/gpu, so placement is the only signal they give.
	NodeSelector map[string]string `json:"nodeSelector"`
	Affinity     *Affinity         `json:"affinity"`
}

type Affinity struct {
	NodeAffinity *NodeAffinity `json:"nodeAffinity"`
}

// NodeAffinity decodes only the required form. A preferred rule is a hint the
// scheduler may ignore, so it cannot establish that a workload is GPU-backed.
type NodeAffinity struct {
	Required *NodeSelectorSpec `json:"requiredDuringSchedulingIgnoredDuringExecution"`
}

type NodeSelectorSpec struct {
	NodeSelectorTerms []NodeSelectorTerm `json:"nodeSelectorTerms"`
}

type NodeSelectorTerm struct {
	MatchExpressions []NodeSelectorRequirement `json:"matchExpressions"`
	MatchFields      []NodeSelectorRequirement `json:"matchFields"`
}

type NodeSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type Container struct {
	Image string `json:"image"`

	// Resources carries only what GPU detection needs. Quantities are always
	// serialised as JSON strings, so a string map decodes them without pulling in
	// apimachinery just to parse "1".
	Resources ResourceRequirements `json:"resources"`
}

type ResourceRequirements struct {
	Limits   map[string]string `json:"limits"`
	Requests map[string]string `json:"requests"`
}

// WorkloadStatus carries the readiness counters of all three kinds. Deployment and
// StatefulSet report replicas/readyReplicas; DaemonSet reports desiredNumberScheduled/
// numberReady. A field the kind does not use is simply absent and decodes to zero.
type WorkloadStatus struct {
	Replicas               int `json:"replicas"`
	ReadyReplicas          int `json:"readyReplicas"`
	DesiredNumberScheduled int `json:"desiredNumberScheduled"`
	NumberReady            int `json:"numberReady"`
}

// ListWorkloads returns every Deployment, StatefulSet and DaemonSet in the cluster.
// These are cluster-wide reads, so they need a ClusterRole granting get/list on
// deployments, statefulsets and daemonsets in the apps group.
//
// The three kinds are fetched concurrently under one timeout. A kind that fails does
// not discard the other two: whatever was read is returned alongside the joined error,
// so a partial answer still reaches the page.
func (c *Client) ListWorkloads(ctx context.Context) (*WorkloadList, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	kinds := []struct {
		kind string
		path string
		what string
	}{
		{KindDeployment, deploymentsPath, "deployment list"},
		{KindStatefulSet, statefulSetsPath, "statefulset list"},
		{KindDaemonSet, daemonSetsPath, "daemonset list"},
	}

	var (
		mu   sync.Mutex
		out  WorkloadList
		errs = make([]error, len(kinds))
		wg   sync.WaitGroup
	)
	for i, k := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var list WorkloadList
			if err := c.GetJSON(ctx, k.path, k.what, &list); err != nil {
				errs[i] = err
				return
			}
			for j := range list.Items {
				list.Items[j].Kind = k.kind
			}
			mu.Lock()
			out.Items = append(out.Items, list.Items...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	return &out, errors.Join(errs...)
}

package kube

import (
	"bytes"
	"context"
	"encoding/json"
)

const nodesPath = "/api/v1/nodes"

// ResourceGPU is the NVIDIA device-plugin capacity key. It is used instead of a
// nodegroup label because it is standard and portable across clusters.
const ResourceGPU = "nvidia.com/gpu"

type NodeList struct {
	Items []Node `json:"items"`
}

// Node decodes only the fields the capacity section renders.
type Node struct {
	Metadata NodeMetadata `json:"metadata"`
	Status   NodeStatus   `json:"status"`
}

type NodeMetadata struct {
	Name string `json:"name"`
	// Labels are needed to work out which nodes a workload can be scheduled onto.
	Labels map[string]string `json:"labels"`
}

type NodeStatus struct {
	// Capacity is decoded whole rather than field by field. Which accelerator a
	// cluster advertises is not knowable in advance: NVIDIA full cards, NVIDIA MIG
	// slices, AMD, Intel, AWS Neuron and TPUs all use different resource names.
	Capacity map[string]Quantity `json:"capacity"`
	NodeInfo NodeInfo            `json:"nodeInfo"`
}

type NodeInfo struct {
	Architecture string `json:"architecture"`
}

// Quantity is a Kubernetes resource quantity. The API serves them as strings, but a
// bare number is accepted too so one odd node cannot fail the whole decode.
type Quantity string

func (q *Quantity) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*q = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*q = Quantity(s)
		return nil
	}
	*q = Quantity(b)
	return nil
}

// ListNodes returns every node in the cluster. Nodes are cluster-scoped, so this
// needs a ClusterRole granting get/list on nodes.
func (c *Client) ListNodes(ctx context.Context) (*NodeList, error) {
	var list NodeList
	if err := c.GetJSON(ctx, FromCache(nodesPath), "node list", &list); err != nil {
		return nil, err
	}
	return &list, nil
}

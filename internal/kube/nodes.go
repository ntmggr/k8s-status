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

// LabelZone is the GA topology label. LabelZoneLegacy is checked as a fallback for
// pre-1.17 clusters that have not been relabelled.
const LabelZone = "topology.kubernetes.io/zone"
const LabelZoneLegacy = "failure-domain.beta.kubernetes.io/zone" // pre-1.17 clusters

type NodeList struct {
	Items []Node `json:"items"`
}

// Node decodes only the fields the capacity section renders.
type Node struct {
	Metadata NodeMetadata `json:"metadata"`
	Spec     NodeSpec     `json:"spec"`
	Status   NodeStatus   `json:"status"`
}

// NodeSpec decodes only ProviderID, used to tell which cloud (and which managed
// offering on it) the cluster runs on.
type NodeSpec struct {
	ProviderID string `json:"providerID"`
}

type NodeMetadata struct {
	Name string `json:"name"`
	// Labels are needed to work out which nodes a workload can be scheduled onto.
	Labels map[string]string `json:"labels"`
}

type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// NodeAddress is one entry of status.addresses. Only Type and Address are decoded;
// the rest of the object carries nothing this page renders.
type NodeAddress struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

type NodeStatus struct {
	// Conditions carry readiness. A node the cloud provider has shut down lingers in
	// the list as NotReady, and counting it as capacity overstates what the cluster has.
	Conditions []NodeCondition `json:"conditions"`
	// Capacity is decoded whole rather than field by field. Which accelerator a
	// cluster advertises is not knowable in advance: NVIDIA full cards, NVIDIA MIG
	// slices, AMD, Intel, AWS Neuron and TPUs all use different resource names.
	Capacity map[string]Quantity `json:"capacity"`
	NodeInfo NodeInfo            `json:"nodeInfo"`
	// Addresses carries the node's InternalIP, used elsewhere to join running pods
	// to the node they landed on via status.hostIP.
	Addresses []NodeAddress `json:"addresses"`
}

type NodeInfo struct {
	Architecture string `json:"architecture"`
	// KubeletVersion carries the managed-offering marker too (e.g. "v1.32.3-eks-...",
	// "v1.29.0-gke.1234000"), which is more reliable than guessing from ProviderID
	// alone: ProviderID says which cloud, not which managed control plane on it.
	KubeletVersion string `json:"kubeletVersion"`
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

// Ready reports the kubelet's own verdict. Anything other than an explicit True, which
// includes the Unknown a shut-down node reports, is not ready.
func (n Node) Ready() bool {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

// Zone reports the node's availability zone, checking the GA label first and
// falling back to the pre-1.17 label. "" means neither is set.
func (n Node) Zone() string {
	if z := n.Metadata.Labels[LabelZone]; z != "" {
		return z
	}
	return n.Metadata.Labels[LabelZoneLegacy]
}

// InternalIPs returns every address the node advertises as InternalIP. Dual-stack
// nodes carry more than one; ExternalIP and Hostname entries are not addresses a
// pod's hostIP will ever match.
func (n Node) InternalIPs() []string {
	var ips []string
	for _, a := range n.Status.Addresses {
		if a.Type == "InternalIP" {
			ips = append(ips, a.Address)
		}
	}
	return ips
}

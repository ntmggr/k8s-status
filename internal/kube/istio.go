package kube

import "context"

// Istio serves PeerAuthentication under security.istio.io. v1 is the current stable
// version; v1beta1 is kept as a fallback for older Istio installs that have not yet
// moved this CRD to v1.
const (
	IstioSecurityGroupVersion     = "security.istio.io/v1"
	IstioSecurityGroupVersionBeta = "security.istio.io/v1beta1"

	ResourcePeerAuthentications = "peerauthentications"

	IstioSystemNamespace          = "istio-system"
	PeerAuthenticationDefaultName = "default"
)

// PeerAuthentication decodes the fields needed to resolve mTLS at every scope Istio
// defines: mesh-wide (the "default"-named object in the root namespace, MeshPolicy's
// single-object read below), namespace-wide (an object in a workload's own namespace
// with an empty selector) and workload-specific (same namespace, a selector matching
// the workload's pod labels). Metadata.Namespace and Selector only matter for the
// latter two, resolved against a whole-cluster list rather than the single-object GET;
// see ResolveServicePolicy in internal/status/mesh.go. Nothing else on the object
// (port-level overrides, mTLS profiles' other fields) is read.
// https://istio.io/latest/docs/reference/config/security/peer_authentication/
type PeerAuthentication struct {
	Metadata PeerAuthenticationMetadata `json:"metadata"`
	Spec     PeerAuthenticationSpec     `json:"spec"`
}

type PeerAuthenticationMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type PeerAuthenticationSpec struct {
	MTLS PeerAuthenticationMTLS `json:"mtls"`
	// Selector decodes matchLabels directly rather than as a bag of unknown shape:
	// Istio's WorkloadSelector has exactly that one field, and the labels themselves
	// are what a workload-specific override is matched against.
	Selector PeerAuthenticationSelector `json:"selector"`
}

// PeerAuthenticationSelector is Istio's WorkloadSelector: matchLabels only, no
// matchExpressions. An empty MatchLabels means the object has no selector at all,
// which is exactly what makes it namespace-wide rather than workload-specific.
type PeerAuthenticationSelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

type PeerAuthenticationMTLS struct {
	Mode string `json:"mode"`
}

// PeerAuthenticationList is the cluster-wide collection response, the same shape as
// PodList/NodeList: every PeerAuthentication in every namespace, in one call.
type PeerAuthenticationList struct {
	Items []PeerAuthentication `json:"items"`
}

// DetectIstio reports which PeerAuthentication group/version the API server serves,
// preferring v1 and falling back to v1beta1. "" with a nil error means neither is
// served, mirroring the Flux auto-detection degradation style: this reads discovery,
// which needs no extra RBAC, and an absent CRD is a normal answer, not a fault.
func (c *Client) DetectIstio(ctx context.Context) (string, error) {
	ok, err := c.HasResource(ctx, IstioSecurityGroupVersion, ResourcePeerAuthentications)
	if err != nil {
		return "", err
	}
	if ok {
		return IstioSecurityGroupVersion, nil
	}

	ok, err = c.HasResource(ctx, IstioSecurityGroupVersionBeta, ResourcePeerAuthentications)
	if err != nil {
		return "", err
	}
	if ok {
		return IstioSecurityGroupVersionBeta, nil
	}

	return "", nil
}

// peerAuthenticationPath is the single-object path for the mesh-wide policy: the
// "default"-named PeerAuthentication in namespace, not a list.
func peerAuthenticationPath(groupVersion, namespace string) string {
	return "/apis/" + groupVersion + "/namespaces/" + namespace + "/peerauthentications/" + PeerAuthenticationDefaultName
}

// peerAuthenticationsPath is the cluster-wide collection endpoint: every
// PeerAuthentication in every namespace, the same unnamespaced shape
// deploymentsPath/statefulSetsPath/daemonSetsPath already use in workloads.go.
func peerAuthenticationsPath(groupVersion string) string {
	return "/apis/" + groupVersion + "/peerauthentications"
}

// MeshPolicy reads the mesh-wide PeerAuthentication object by name. It is a
// single-object GET, not a list, so FromCache does not apply: that helper trades a
// quorum read for a watch-cache read of a whole collection, and there is no
// collection here to trade away. A GET by name is already the cheapest read shape,
// and the smallest namespaced Role this project grants: get on one named object.
func (c *Client) MeshPolicy(ctx context.Context, groupVersion, namespace string) (*PeerAuthentication, error) {
	var pa PeerAuthentication
	if err := c.GetJSON(ctx, peerAuthenticationPath(groupVersion, namespace), "peerauthentication", &pa); err != nil {
		return nil, err
	}
	return &pa, nil
}

// ListPeerAuthentications returns every PeerAuthentication in every namespace, so a
// per-service mTLS answer can be resolved against namespace- and workload-scoped
// overrides the single-object mesh-wide read above cannot see. Cluster-scoped list,
// following the same shape as ListNodes/ListWorkloads: this is the permission
// increase the per-service filter needs, get on the mesh-wide object alone cannot
// serve it, since overrides can live in any namespace.
func (c *Client) ListPeerAuthentications(ctx context.Context, groupVersion string) (*PeerAuthenticationList, error) {
	var list PeerAuthenticationList
	if err := c.GetJSON(ctx, FromCache(peerAuthenticationsPath(groupVersion)), "peerauthentication list", &list); err != nil {
		return nil, err
	}
	return &list, nil
}

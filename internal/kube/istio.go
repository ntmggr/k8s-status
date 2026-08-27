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

// PeerAuthentication decodes only the two fields that decide the mesh-wide mTLS
// posture. Selector is decoded purely to detect presence: a non-empty selector means
// this "default"-named object is actually scoped to a workload, not the whole mesh,
// and reporting it as mesh-wide would be wrong. Nothing else on the object (namespace
// selector, port-level overrides, metadata) is read.
type PeerAuthentication struct {
	Spec PeerAuthenticationSpec `json:"spec"`
}

type PeerAuthenticationSpec struct {
	MTLS PeerAuthenticationMTLS `json:"mtls"`
	// Selector is decoded as a bag of unknown shape: only its presence matters, never
	// what it contains.
	Selector map[string]any `json:"selector"`
}

type PeerAuthenticationMTLS struct {
	Mode string `json:"mode"`
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

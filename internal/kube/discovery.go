package kube

import (
	"context"
	"errors"
	"net/http"
)

// ArgoCD's Application group/version, used for discovery only.
const (
	ArgoCDGroupVersion = "argoproj.io/v1alpha1"
	ResourceApps       = "applications"
)

// APIResourceList is the discovery answer for one group/version.
type APIResourceList struct {
	Resources []APIResource `json:"resources"`
}

type APIResource struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// HasResource reports whether the API server serves resource in groupVersion.
//
// It reads the discovery endpoint rather than the CRD object itself, because every
// authenticated client may read discovery: detecting which GitOps controllers a cluster
// runs therefore needs no permission on customresourcedefinitions. A 404 means the group
// is not served at all, which is the normal answer for an absent CRD and not an error.
func (c *Client) HasResource(ctx context.Context, groupVersion, resource string) (bool, error) {
	var list APIResourceList
	if err := c.GetJSON(ctx, "/apis/"+groupVersion, "api resource list", &list); err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	for _, r := range list.Resources {
		if r.Name == resource {
			return true, nil
		}
	}
	return false, nil
}

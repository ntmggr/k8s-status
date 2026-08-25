package argocd

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ntmggr/k8s-status/internal/kube"
)

const (
	DefaultTokenPath = kube.DefaultTokenPath
	DefaultCAPath    = kube.DefaultCAPath
)

// Client reads ArgoCD Applications from the local cluster's API server.
// BaseURL, TokenPath and HTTPClient are exported so tests can point at an httptest server.
type Client struct {
	BaseURL    string
	Namespace  string
	TokenPath  string
	HTTPClient *http.Client
}

// New builds an in-cluster client from the standard service account mount.
func New(namespace string) (*Client, error) {
	kc, err := kube.New()
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:    kc.BaseURL,
		Namespace:  namespace,
		TokenPath:  kc.TokenPath,
		HTTPClient: kc.HTTPClient,
	}, nil
}

func (c *Client) kube() *kube.Client {
	return &kube.Client{BaseURL: c.BaseURL, TokenPath: c.TokenPath, HTTPClient: c.HTTPClient}
}

func (c *Client) applicationsPath() string {
	return fmt.Sprintf("/apis/argoproj.io/v1alpha1/namespaces/%s/applications", c.Namespace)
}

// ListApplications returns every Application in the configured namespace.
func (c *Client) ListApplications(ctx context.Context) (*ApplicationList, error) {
	var list ApplicationList
	if err := c.kube().GetJSON(ctx, c.applicationsPath(), "application list", &list); err != nil {
		return nil, err
	}
	return &list, nil
}

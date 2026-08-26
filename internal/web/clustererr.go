package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// ClusterError is a read failure translated into something a person can act on.
// The raw text is kept in Detail: it is what you need when the guess is wrong.
type ClusterError struct {
	Title  string
	Hint   string
	Detail string
}

// classifyClusterError maps a failed cluster read onto a cause and a next step.
// The cases here are the ones that actually happen: an expired local credential,
// missing RBAC, ArgoCD not installed, and the API server being unreachable.
func classifyClusterError(err error) *ClusterError {
	if err == nil {
		return nil
	}
	raw := err.Error()
	low := strings.ToLower(raw)
	out := &ClusterError{Detail: truncate(raw, 300)}

	var se *kube.StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusUnauthorized:
			out.Title = "Not authenticated to the cluster"
			out.Hint = "The ServiceAccount token was rejected. In a cluster this usually means the token expired or was not mounted; running locally it usually means the proxy is not authenticated."
			return out
		case http.StatusForbidden:
			out.Title = "Not allowed to read ArgoCD Applications"
			out.Hint = "The ServiceAccount needs get and list on applications.argoproj.io in the argocd namespace. Apply the Role and RoleBinding from the chart, or deploy/install.yaml."
			return out
		case http.StatusNotFound:
			out.Title = "ArgoCD is not installed here"
			out.Hint = "No applications.argoproj.io resource on this cluster. Point ARGOCD_NAMESPACE at the right namespace, or set SOURCES=flux if this cluster uses Flux."
			return out
		}
	}

	switch {
	// The exec credential plugin failing is the single most common local failure:
	// an expired SSO session shows up as a 500 from kubectl proxy, which reads as
	// a server fault when it is actually a login on this machine.
	case strings.Contains(low, "getting credentials"),
		strings.Contains(low, "exec plugin"),
		strings.Contains(low, "executable aws"):
		out.Title = "Your cluster credentials have expired"
		out.Hint = "This is a login on your machine, not a problem with the cluster. Refresh it (for AWS SSO: aws sso login), restart kubectl proxy, then reload this page."
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "no such host"),
		strings.Contains(low, "network is unreachable"):
		out.Title = "Cannot reach the Kubernetes API"
		out.Hint = "Nothing answered at the API address. Running locally, check that kubectl proxy is still up; in a cluster, check the ServiceAccount and network policy."
	case strings.Contains(low, "context deadline exceeded"),
		strings.Contains(low, "timeout"), strings.Contains(low, "timed out"):
		out.Title = "The cluster did not answer in time"
		out.Hint = "The API server was reachable but slow. This often clears on its own; the page will retry on the next refresh."
	case strings.Contains(low, "certificate"), strings.Contains(low, "x509"):
		out.Title = "The cluster's certificate was rejected"
		out.Hint = "TLS to the API server could not be verified. In a cluster this means the mounted CA bundle is wrong or missing."
	default:
		out.Title = "Could not read the cluster"
		out.Hint = "The details below are the raw error from the Kubernetes API."
	}
	return out
}

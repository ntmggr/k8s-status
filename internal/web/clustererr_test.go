package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func TestClassifyClusterError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantTitle string
		wantHint  string // substring
	}{
		{
			name:      "expired local credentials read as a server fault",
			err:       errors.New(`kubernetes api returned 500: getting credentials: exec: executable aws failed with exit code 255`),
			wantTitle: "Your cluster credentials have expired",
			wantHint:  "aws sso login",
		},
		{
			name:      "forbidden names the exact permission",
			err:       &kube.StatusError{Code: http.StatusForbidden},
			wantTitle: "Not allowed to read ArgoCD Applications",
			wantHint:  "applications.argoproj.io",
		},
		{
			name:      "unauthorized",
			err:       &kube.StatusError{Code: http.StatusUnauthorized},
			wantTitle: "Not authenticated to the cluster",
		},
		{
			name:      "not found means argocd is absent",
			err:       &kube.StatusError{Code: http.StatusNotFound},
			wantTitle: "ArgoCD is not installed here",
			wantHint:  "SOURCES=flux",
		},
		{
			name:      "connection refused",
			err:       errors.New("dial tcp 127.0.0.1:8001: connect: connection refused"),
			wantTitle: "Cannot reach the Kubernetes API",
		},
		{
			name:      "timeout",
			err:       errors.New("context deadline exceeded"),
			wantTitle: "The cluster did not answer in time",
		},
		{
			name:      "unknown falls back without pretending",
			err:       errors.New("something nobody predicted"),
			wantTitle: "Could not read the cluster",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyClusterError(c.err)
			if got == nil {
				t.Fatal("want a classification, got nil")
			}
			if got.Title != c.wantTitle {
				t.Errorf("title = %q, want %q", got.Title, c.wantTitle)
			}
			if c.wantHint != "" && !strings.Contains(got.Hint, c.wantHint) {
				t.Errorf("hint = %q, want it to mention %q", got.Hint, c.wantHint)
			}
			// The raw text must survive: the guess is sometimes wrong.
			if got.Detail == "" {
				t.Error("want the raw error kept in Detail")
			}
		})
	}
	if classifyClusterError(nil) != nil {
		t.Error("nil error must classify to nil")
	}
}

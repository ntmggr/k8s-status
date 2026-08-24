package kube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("unit-test-placeholder"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return &Client{BaseURL: srv.URL, TokenPath: p, HTTPClient: srv.Client()}
}

func TestListNodesDecodesOnlyTheFieldsUsed(t *testing.T) {
	var gotPath string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"items":[
		  {"metadata":{"name":"n1","labels":{"kubernetes.io/arch":"arm64"}},
		   "spec":{"providerID":"aws:///x"},
		   "status":{"capacity":{"cpu":"8","memory":"32Gi","nvidia.com/gpu":"4"},
		             "nodeInfo":{"architecture":"arm64","kubeletVersion":"v1.31.0"}}}]}`))
	}))

	list, err := c.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if gotPath != nodesPath {
		t.Errorf("path = %q, want %q", gotPath, nodesPath)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Items))
	}
	n := list.Items[0]
	if n.Metadata.Name != "n1" || n.Status.NodeInfo.Architecture != "arm64" || n.Status.Capacity.NvidiaGPU != "4" {
		t.Errorf("node = %+v", n)
	}
}

func TestListNodesForbiddenIsAStatusError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nodes is forbidden: User \"system:serviceaccount:srv-status:srv-status\"\ncannot list resource"))
	}))

	_, err := c.ListNodes(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a *StatusError", err)
	}
	if se.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", se.Code)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error body should be flattened: %q", err.Error())
	}
}

func TestListNodesMalformedJSON(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items": [ {"status": `))
	}))

	if _, err := c.ListNodes(context.Background()); err == nil {
		t.Fatal("want decode error")
	} else if !strings.Contains(err.Error(), "decode node list") {
		t.Errorf("error = %v", err)
	}
}

func TestNewRequiresClusterEnv(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if _, err := New(); err == nil {
		t.Fatal("want error when KUBERNETES_SERVICE_HOST is unset")
	}
}

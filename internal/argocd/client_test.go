package argocd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const placeholderToken = "unit-test-placeholder"

func tokenFile(t *testing.T, value string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(value), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return p
}

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{
		BaseURL:    srv.URL,
		Namespace:  "argocd",
		TokenPath:  tokenFile(t, placeholderToken),
		HTTPClient: srv.Client(),
	}, srv
}

func TestListApplicationsParsesFixture(t *testing.T) {
	var gotPath, gotAuth, gotAccept string

	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotAccept = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, "../../testdata/applications.json")
	}))

	list, err := c.ListApplications(context.Background())
	if err != nil {
		t.Fatalf("ListApplications: %v", err)
	}

	if want := "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := "Bearer " + placeholderToken; gotAuth != want {
		t.Errorf("authorization = %q, want %q", gotAuth, want)
	}
	if gotAccept != "application/json" {
		t.Errorf("accept = %q", gotAccept)
	}
	if len(list.Items) != 15 {
		t.Fatalf("items = %d, want 15", len(list.Items))
	}

	byName := map[string]Application{}
	for _, a := range list.Items {
		byName[a.Metadata.Name] = a
	}

	root, ok := byName["root-app"]
	if !ok {
		t.Fatal("root application missing")
	}
	if root.Metadata.Labels["EnvType"] != "dev" {
		t.Errorf("root EnvType label = %q", root.Metadata.Labels["EnvType"])
	}
	if root.Spec.Source.TargetRevision != "develop" || root.Spec.Source.RepoURL == "" || root.Spec.Source.Path == "" {
		t.Errorf("root source = %+v", root.Spec.Source)
	}
	if root.Status.OperationState.Phase != "Succeeded" || root.Status.OperationState.FinishedAt == "" {
		t.Errorf("root operationState = %+v", root.Status.OperationState)
	}
	if len(root.Status.History) != 2 || root.Status.History[1].ID != 1048 || root.Status.History[1].DeployedAt != "2026-08-21T11:30:57Z" {
		t.Errorf("root history = %+v", root.Status.History)
	}

	pruning := 0
	for _, r := range root.Status.Resources {
		if r.RequiresPruning {
			pruning++
		}
		if r.Kind != "Application" {
			t.Errorf("unexpected resource kind %q", r.Kind)
		}
	}
	if pruning != 2 {
		t.Errorf("requiresPruning count = %d, want 2", pruning)
	}

	if got := byName["media-encoder"].Status.Health.Message; got != "0/2 replicas available" {
		t.Errorf("health message = %q", got)
	}
	if got := byName["session-store"].Status.Sync.Revision; got != "" {
		t.Errorf("null revision decoded to %q, want empty", got)
	}
	if got := byName["search-api"].Status.Health.Status; got != "" {
		t.Errorf("empty health decoded to %q", got)
	}
}

func TestListApplicationsServerError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal\nserver error"))
	}))

	_, err := c.ListApplications(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error body should be flattened: %q", err.Error())
	}
}

func TestListApplicationsMalformedJSON(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items": [ {"metadata": `))
	}))

	if _, err := c.ListApplications(context.Background()); err == nil {
		t.Fatal("want decode error")
	} else if !strings.Contains(err.Error(), "decode application list") {
		t.Errorf("error = %v", err)
	}
}

func TestListApplicationsContextTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := c.ListApplications(ctx); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestListApplicationsMissingToken(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	c.TokenPath = filepath.Join(t.TempDir(), "does-not-exist")

	if _, err := c.ListApplications(context.Background()); err == nil {
		t.Fatal("want token read error")
	} else if !strings.Contains(err.Error(), "service account token") {
		t.Errorf("error = %v", err)
	}
}

func TestNewRequiresClusterEnv(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if _, err := New("argocd"); err == nil {
		t.Fatal("want error when KUBERNETES_SERVICE_HOST is unset")
	}
}

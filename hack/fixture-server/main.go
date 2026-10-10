// Command fixture-server answers the Kubernetes API reads k8s-status makes with the
// JSON files in testdata/, so the page runs offline (scripts/local-test.sh fixture).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	podsPath = "/api/v1/pods"
	podsFile = "pods.json"

	phaseSelectorKey = "status.phase"
	shutdownTimeout  = 5 * time.Second
	readHeaderLimit  = 5 * time.Second
)

// routes maps each API path k8s-status reads to the testdata file that answers it.
// Query strings (resourceVersion, limit, continue) are ignored: every list is served
// whole in one page with no continue token.
//
// accounts-api owns the Job and CronJob, proving batch workloads show up per
// service. The Flux collections are served regardless of SOURCES; podinfo and
// network-policy-controller prove a Flux-managed workload lands in the service
// table. peerauthentications.json covers all three precedence levels: mesh-wide
// STRICT, namespace-wide PERMISSIVE (search-api) and workload DISABLE (admin-ui).
var routes = map[string]string{
	"/apis/argoproj.io/v1alpha1/namespaces/argocd/applications": "applications.json",
	"/api/v1/nodes":                                       "nodes.json",
	"/apis/apps/v1/deployments":                           "deployments.json",
	"/apis/apps/v1/statefulsets":                          "statefulsets.json",
	"/apis/apps/v1/daemonsets":                            "daemonsets.json",
	"/apis/batch/v1/jobs":                                 "jobs.json",
	"/apis/batch/v1/cronjobs":                             "cronjobs.json",
	"/apis/helm.toolkit.fluxcd.io/v2/helmreleases":        "helmreleases.json",
	"/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations": "kustomizations.json",
	"/apis/security.istio.io/v1":                          "istio-discovery.json",
	"/apis/security.istio.io/v1/peerauthentications":      "peerauthentications.json",
	"/apis/security.istio.io/v1/namespaces/istio-system/peerauthentications/default": "peerauthentication.json",
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8001", "listen address")
	dir := flag.String("dir", "testdata", "directory holding the fixture JSON files")
	flag.Parse()

	if err := run(*addr, *dir); err != nil {
		log.Fatalf("fixture-server: %v", err)
	}
}

func run(addr, dir string) error {
	handler, err := newHandler(os.DirFS(dir))
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: readHeaderLimit}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("fixture-server: serving %s on http://%s", dir, addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newHandler loads every fixture up front, so a missing or malformed file fails at
// startup instead of surfacing as a half-rendered page.
func newHandler(fsys fs.FS) (http.Handler, error) {
	mux := http.NewServeMux()
	for path, file := range routes {
		body, err := loadJSON(fsys, file)
		if err != nil {
			return nil, err
		}
		mux.Handle("GET "+path, serveBody(body))
	}

	pods, err := loadPods(fsys)
	if err != nil {
		return nil, err
	}
	mux.Handle("GET "+podsPath, pods)
	mux.HandleFunc("/", notFound)
	return mux, nil
}

func loadJSON(fsys fs.FS, file string) ([]byte, error) {
	body, err := fs.ReadFile(fsys, file)
	if err != nil {
		return nil, fmt.Errorf("read fixture: %w", err)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("fixture %s is not valid JSON", file)
	}
	return body, nil
}

func serveBody(body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, body)
	})
}

// podsHandler honours fieldSelector=status.phase=X like the real API server does.
// k8s-status asks for Running and Pending pods separately and counts each list as
// what it asked for, so serving every pod to both would double-count.
type podsHandler struct {
	all     []json.RawMessage
	byPhase map[string][]json.RawMessage
}

func loadPods(fsys fs.FS) (*podsHandler, error) {
	body, err := loadJSON(fsys, podsFile)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode %s: %w", podsFile, err)
	}

	h := &podsHandler{all: list.Items, byPhase: map[string][]json.RawMessage{}}
	for i, item := range list.Items {
		var pod struct {
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		}
		if err := json.Unmarshal(item, &pod); err != nil {
			return nil, fmt.Errorf("decode %s item %d: %w", podsFile, i, err)
		}
		h.byPhase[pod.Status.Phase] = append(h.byPhase[pod.Status.Phase], item)
	}
	return h, nil
}

func (h *podsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	items := h.all
	if selector := r.URL.Query().Get("fieldSelector"); selector != "" {
		phase, ok := parsePhaseSelector(selector)
		if !ok {
			log.Printf("fixture-server: unsupported fieldSelector %q on %s", selector, r.URL.Path)
			writeStatus(w, http.StatusBadRequest, "BadRequest", "only status.phase field selectors are supported")
			return
		}
		items = h.byPhase[phase]
	}
	if items == nil {
		items = []json.RawMessage{}
	}

	body, err := json.Marshal(map[string]any{
		"kind": "PodList", "apiVersion": "v1", "metadata": map[string]any{}, "items": items,
	})
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "encode pod list")
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// parsePhaseSelector accepts the two equality forms the API server does:
// status.phase=X and status.phase==X.
func parsePhaseSelector(selector string) (string, bool) {
	key, value, found := strings.Cut(selector, "=")
	if !found || key != phaseSelectorKey {
		return "", false
	}
	value = strings.TrimPrefix(value, "=")
	if value == "" || strings.ContainsAny(value, ",=!") {
		return "", false
	}
	return value, true
}

func notFound(w http.ResponseWriter, r *http.Request) {
	log.Printf("fixture-server: no fixture for %s %s", r.Method, r.URL.Path)
	writeStatus(w, http.StatusNotFound, "NotFound", "no fixture for this path")
}

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	body, _ := json.Marshal(map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "message": message, "code": code,
	})
	writeJSON(w, code, body)
}

func writeJSON(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

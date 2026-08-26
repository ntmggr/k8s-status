package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ntmggr/k8s-status/internal/argocd"
	"github.com/ntmggr/k8s-status/internal/kube"
	"github.com/ntmggr/k8s-status/internal/status"
	"github.com/ntmggr/k8s-status/internal/web"
)

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	envName := env("ENV_NAME", "unknown")
	envType := env("ENV_TYPE", "")
	clusterName := env("CLUSTER_NAME", "")
	region := env("REGION", "")
	basePath := web.NormalizeBasePath(env("BASE_PATH", "/k8s-status"))
	sources, auto := parseSources(env("SOURCES", string(status.SourceArgoCD)))
	if auto {
		sources = detectSources()
	}
	useArgoCD := hasSource(sources, status.SourceArgoCD)
	useFlux := hasSource(sources, status.SourceFlux)
	namespace := env("ARGOCD_NAMESPACE", "argocd")
	rootApp := env("ROOT_APP_NAME", "")
	argocdUI := env("ARGOCD_UI_BASE", "")
	ignoreGlobs := splitGlobs(env("IGNORE_GLOBS", ""))
	gpuGlobs := splitGlobs(env("GPU_GLOBS", defaultGPUGlobs))
	nodeStats := envBool("NODE_STATS", false)
	unmanaged := envBool("UNMANAGED", false)
	unmanagedIgnoreNS := splitGlobs(env("UNMANAGED_IGNORE_NS", ""))
	cacheTTL := time.Duration(envInt("CACHE_TTL_SECONDS", 15)) * time.Second
	refresh := envInt("REFRESH_SECONDS", 30)
	port := env("PORT", "8080")

	// lister stays nil when argocd is not one of SOURCES, so its API is never called.
	var lister status.Lister
	if useArgoCD {
		l, lerr := buildLister(namespace)
		if lerr != nil {
			log.Printf("kubernetes client unavailable: %v (serving degraded page)", lerr)
			l = failingLister{err: lerr}
		}
		lister = l
	}

	collector := status.NewCollector(lister, status.Options{
		Sources:           sources,
		RootAppName:       rootApp,
		IgnoreGlobs:       ignoreGlobs,
		GPUGlobs:          gpuGlobs,
		UnmanagedIgnoreNS: unmanagedIgnoreNS,
	}, cacheTTL)

	if useFlux {
		fluxLister, ferr := buildFluxLister()
		if ferr != nil {
			log.Printf("flux enabled but the flux client is unavailable: %v", ferr)
			fluxLister = failingFluxLister{err: ferr}
		}
		collector.WithFlux(fluxLister)
	}

	if nodeStats {
		nodeLister, nerr := buildNodeLister()
		if nerr != nil {
			log.Printf("node stats enabled but the nodes client is unavailable: %v", nerr)
			nodeLister = failingNodeLister{err: nerr}
		}
		collector.WithNodes(nodeLister)
	}

	if unmanaged {
		workloadLister, werr := buildWorkloadLister()
		if werr != nil {
			log.Printf("unmanaged workloads enabled but the workloads client is unavailable: %v", werr)
			workloadLister = failingWorkloadLister{err: werr}
		}
		collector.WithUnmanaged(workloadLister)
	}

	srv, err := web.NewServer(web.Config{
		LocalMode:      os.Getenv("KUBE_API_URL") != "",
		EnvName:        envName,
		EnvType:        envType,
		Region:         region,
		ClusterName:    clusterName,
		BasePath:       basePath,
		ArgoCDUIBase:   argocdUI,
		RefreshSeconds: refresh,
		BuildVersion:   version,
	}, collector)
	if err != nil {
		log.Fatalf("build server: %v", err)
	}

	log.Printf("k8s-status %s starting: env=%q type=%q region=%q cluster=%q base=%q sources=%v namespace=%q rootApp=%q ttl=%s refresh=%ds port=%s ignore=%v argocdUI=%q nodeStats=%t unmanaged=%t unmanagedIgnoreNS=%v",
		version, envName, envType, region, clusterName, basePath, sources, namespace, rootApp, cacheTTL, refresh, port, ignoreGlobs, argocdUI, nodeStats, unmanaged, unmanagedIgnoreNS)

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("listen: %v", err)
	case <-ctx.Done():
		log.Print("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

// buildLister prefers the in-cluster mount; KUBE_API_URL exists so the binary can be
// pointed at a local `kubectl proxy` during development.
func buildLister(namespace string) (status.Lister, error) {
	if apiURL := os.Getenv("KUBE_API_URL"); apiURL != "" {
		return &argocd.Client{
			BaseURL:   strings.TrimRight(apiURL, "/"),
			Namespace: namespace,
			TokenPath: env("KUBE_TOKEN_FILE", os.DevNull),
		}, nil
	}
	return argocd.New(namespace)
}

// buildKubeClient mirrors buildLister for the cluster-scoped reads. It is only called
// by the opt-in features, so the default deployment never builds it.
func buildKubeClient() (*kube.Client, error) {
	if apiURL := os.Getenv("KUBE_API_URL"); apiURL != "" {
		return &kube.Client{
			BaseURL:   strings.TrimRight(apiURL, "/"),
			TokenPath: env("KUBE_TOKEN_FILE", os.DevNull),
		}, nil
	}
	return kube.New()
}

// buildNodeLister is only called when NODE_STATS is on: nodes are cluster-scoped.
func buildNodeLister() (status.NodeLister, error) {
	c, err := buildKubeClient()
	if err != nil {
		return nil, err
	}
	return c, nil
}

// buildFluxLister is only called when flux is one of SOURCES: HelmReleases and
// Kustomizations are spread across every namespace, so this is a cluster-wide read.
func buildFluxLister() (status.FluxLister, error) {
	c, err := buildKubeClient()
	if err != nil {
		return nil, err
	}
	return c, nil
}

// buildWorkloadLister is only called when UNMANAGED is on: listing workloads in every
// namespace is a cluster-wide read.
func buildWorkloadLister() (status.WorkloadLister, error) {
	c, err := buildKubeClient()
	if err != nil {
		return nil, err
	}
	return c, nil
}

type failingLister struct{ err error }

func (f failingLister) ListApplications(context.Context) (*argocd.ApplicationList, error) {
	return nil, f.err
}

type failingFluxLister struct{ err error }

func (f failingFluxLister) ListFlux(context.Context) (*kube.FluxList, error) {
	return nil, f.err
}

type failingNodeLister struct{ err error }

func (f failingNodeLister) ListNodes(context.Context) (*kube.NodeList, error) {
	return nil, f.err
}

type failingWorkloadLister struct{ err error }

func (f failingWorkloadLister) ListWorkloads(context.Context) (*kube.WorkloadList, error) {
	return nil, f.err
}

// parseSources reads SOURCES: a comma-separated list of "argocd", "flux", or "auto".
//
// An unrecognised entry is logged and ignored rather than aborting startup — a typo in
// one environment variable must not take the page down — and an empty result falls back
// to ArgoCD. "auto" anywhere in the list asks for detection and overrides the rest.
func parseSources(raw string) (srcs []status.Source, auto bool) {
	seen := map[status.Source]bool{}
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		switch s := status.Source(strings.ToLower(trimmed)); s {
		case sourceAuto:
			auto = true
		case status.SourceArgoCD, status.SourceFlux:
			if !seen[s] {
				seen[s] = true
				srcs = append(srcs, s)
			}
		default:
			log.Printf("unknown SOURCES entry %q, ignoring", trimmed)
		}
	}
	if auto {
		return nil, true
	}
	if len(srcs) == 0 {
		return []status.Source{status.SourceArgoCD}, false
	}
	return srcs, false
}

// sourceAuto is not a source of its own: it asks parseSources to defer to detection.
const sourceAuto status.Source = "auto"

// detectSources asks the API server which GitOps CRDs the cluster serves. It reads the
// discovery endpoint, which any authenticated client may read, so this needs no
// permission on customresourcedefinitions. Anything that goes wrong falls back to
// ArgoCD rather than serving a page with no sources at all.
func detectSources() []status.Source {
	c, err := buildKubeClient()
	if err != nil {
		log.Printf("SOURCES=auto but the kubernetes client is unavailable: %v (using argocd)", err)
		return []status.Source{status.SourceArgoCD}
	}

	ctx, cancel := context.WithTimeout(context.Background(), kube.RequestTimeout)
	defer cancel()

	probe := func(groupVersion, resource string) bool {
		ok, perr := c.HasResource(ctx, groupVersion, resource)
		if perr != nil {
			log.Printf("SOURCES=auto: discovery of %s failed: %v", groupVersion, perr)
			return false
		}
		return ok
	}

	var out []status.Source
	if probe(kube.ArgoCDGroupVersion, kube.ResourceApps) {
		out = append(out, status.SourceArgoCD)
	}
	// Either controller alone is enough: a cluster may run kustomize-controller without
	// helm-controller, or the other way round.
	if probe(kube.HelmReleaseGroupVersion, kube.ResourceHelmReleases) ||
		probe(kube.KustomizationGroupVersion, kube.ResourceKustomizations) {
		out = append(out, status.SourceFlux)
	}

	if len(out) == 0 {
		log.Print("SOURCES=auto detected no GitOps CRDs, using argocd")
		return []status.Source{status.SourceArgoCD}
	}
	log.Printf("SOURCES=auto detected %v", out)
	return out
}

func hasSource(sources []status.Source, want status.Source) bool {
	for _, s := range sources {
		if s == want {
			return true
		}
	}
	return false
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("invalid %s=%q, using %d", key, v, def)
		return def
	}
	return n
}

// envBool accepts the usual strconv spellings; anything else keeps the default.
func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		log.Printf("invalid %s=%q, using %t", key, v, def)
		return def
	}
	return b
}

func splitGlobs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// defaultGPUGlobs is deliberately empty. GPU services are detected from what they
// actually request (see status.FillGPU), which needs no site-specific list and cannot
// go stale. GPU_GLOBS remains as an escape hatch for clusters that do not grant the
// workload read the detection needs.
const defaultGPUGlobs = ""

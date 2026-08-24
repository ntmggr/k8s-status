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

	"github.com/ntmggr/srv-status/internal/argocd"
	"github.com/ntmggr/srv-status/internal/kube"
	"github.com/ntmggr/srv-status/internal/status"
	"github.com/ntmggr/srv-status/internal/web"
)

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	envName := env("ENV_NAME", "unknown")
	envType := env("ENV_TYPE", "")
	clusterName := env("CLUSTER_NAME", "")
	region := env("REGION", "")
	basePath := web.NormalizeBasePath(env("BASE_PATH", "/srv-status"))
	namespace := env("ARGOCD_NAMESPACE", "argocd")
	rootApp := env("ROOT_APP_NAME", "ocp-services")
	argocdUI := env("ARGOCD_UI_BASE", "")
	ignoreGlobs := splitGlobs(env("IGNORE_GLOBS", ""))
	gpuGlobs := splitGlobs(env("GPU_GLOBS", defaultGPUGlobs))
	nodeStats := envBool("NODE_STATS", false)
	cacheTTL := time.Duration(envInt("CACHE_TTL_SECONDS", 15)) * time.Second
	refresh := envInt("REFRESH_SECONDS", 30)
	port := env("PORT", "8080")

	lister, err := buildLister(namespace)
	if err != nil {
		log.Printf("kubernetes client unavailable: %v (serving degraded page)", err)
		lister = failingLister{err: err}
	}

	collector := status.NewCollector(lister, status.Options{
		RootAppName: rootApp,
		IgnoreGlobs: ignoreGlobs,
		GPUGlobs:    gpuGlobs,
	}, cacheTTL)

	if nodeStats {
		nodeLister, nerr := buildNodeLister()
		if nerr != nil {
			log.Printf("node stats enabled but the nodes client is unavailable: %v", nerr)
			nodeLister = failingNodeLister{err: nerr}
		}
		collector.WithNodes(nodeLister)
	}

	srv, err := web.NewServer(web.Config{
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

	log.Printf("srv-status %s starting: env=%q type=%q region=%q cluster=%q base=%q namespace=%q rootApp=%q ttl=%s refresh=%ds port=%s ignore=%v argocdUI=%q nodeStats=%t",
		version, envName, envType, region, clusterName, basePath, namespace, rootApp, cacheTTL, refresh, port, ignoreGlobs, argocdUI, nodeStats)

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

// buildNodeLister mirrors buildLister. Nodes are cluster-scoped, so this is only
// called when NODE_STATS is on.
func buildNodeLister() (status.NodeLister, error) {
	if apiURL := os.Getenv("KUBE_API_URL"); apiURL != "" {
		return &kube.Client{
			BaseURL:   strings.TrimRight(apiURL, "/"),
			TokenPath: env("KUBE_TOKEN_FILE", os.DevNull),
		}, nil
	}
	return kube.New()
}

type failingLister struct{ err error }

func (f failingLister) ListApplications(context.Context) (*argocd.ApplicationList, error) {
	return nil, f.err
}

type failingNodeLister struct{ err error }

func (f failingNodeLister) ListNodes(context.Context) (*kube.NodeList, error) {
	return nil, f.err
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

// defaultGPUGlobs marks inference workloads that typically run on GPU nodes.
// Override with GPU_GLOBS; set it to an empty string to disable the marker.
const defaultGPUGlobs = "*-gpu,*triton*,tts-engine-*,parakeet-*,nvidia-*,s2s-*,resemble-*,*-vllm,deepasr*,hybrid-turn-*,logos-*,*-medium,*-large"

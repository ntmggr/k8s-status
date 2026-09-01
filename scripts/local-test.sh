#!/usr/bin/env bash
# Run k8s-status locally. Two modes:
#   ./scripts/local-test.sh fixture             offline, uses testdata/applications.json
#   ./scripts/local-test.sh cluster <context>   live, via kubectl proxy (kube context is required)
#
# Deployment-specific values are not baked in. Override ENV_NAME, ENV_TYPE, REGION,
# CLUSTER_NAME, ARGOCD_UI_BASE, ROOT_APP_NAME, IGNORE_GLOBS, NODE_STATS, UNMANAGED,
# UNMANAGED_IGNORE_NS, MESH_MTLS, MESH_NAMESPACE and AZ_SPREAD in the environment as
# needed.
#
# SOURCES works the same way, inherited from your shell rather than set here:
#   SOURCES=flux ./scripts/local-test.sh fixture
# shows the fixture's Flux HelmRelease/Kustomization data (testdata/helmreleases.json,
# testdata/kustomizations.json) instead of the default ArgoCD Applications.
set -euo pipefail

MODE="${1:-fixture}"
CONTEXT="${2:-}"
PORT="${PORT:-8080}"
PROXY_PORT="${PROXY_PORT:-8001}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PIDS=()

usage() { echo "usage: $0 fixture | $0 cluster <kube-context>" >&2; exit 2; }

cleanup() { for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done; rm -rf "${FAKE_DIR:-}"; }
trap cleanup EXIT INT TERM

port_busy() { lsof -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

# A second concurrent run (e.g. a leftover cluster-mode instance still up in another
# terminal) binding the same PROXY_PORT is exactly the failure mode that produced the
# "Expecting value" JSON traceback here before: two different backends answer the same
# port depending on timing, and k8s-status's own /api/status fetch to whichever one
# wins comes back malformed. Fail fast and say so, instead of racing silently.
for p in "$PORT" "$PROXY_PORT"; do
  if port_busy "$p"; then
    echo "error: port $p is already in use -- stop whatever's still holding it (an" >&2
    echo "earlier local-test.sh run left running?) or override PORT/PROXY_PORT." >&2
    lsof -iTCP:"$p" -sTCP:LISTEN >&2
    exit 1
  fi
done

cd "$ROOT"
go build -o /tmp/k8s-status-local ./cmd/k8s-status

case "$MODE" in
  fixture)
    FAKE_DIR="$(mktemp -d)"
    mkdir -p "$FAKE_DIR/apis/argoproj.io/v1alpha1/namespaces/argocd"
    mkdir -p "$FAKE_DIR/apis/apps/v1" "$FAKE_DIR/api/v1"
    mkdir -p "$FAKE_DIR/apis/security.istio.io/v1/namespaces/istio-system/peerauthentications"
    mkdir -p "$FAKE_DIR/apis/helm.toolkit.fluxcd.io/v2" "$FAKE_DIR/apis/kustomize.toolkit.fluxcd.io/v1"
    cp testdata/applications.json "$FAKE_DIR/apis/argoproj.io/v1alpha1/namespaces/argocd/applications"
    cp testdata/nodes.json        "$FAKE_DIR/api/v1/nodes"
    cp testdata/pods.json         "$FAKE_DIR/api/v1/pods"
    cp testdata/deployments.json  "$FAKE_DIR/apis/apps/v1/deployments"
    cp testdata/statefulsets.json "$FAKE_DIR/apis/apps/v1/statefulsets"
    cp testdata/daemonsets.json   "$FAKE_DIR/apis/apps/v1/daemonsets"
    # Cluster-wide, same as the real Flux API: one collection per kind, no per-namespace
    # split. Served regardless of SOURCES; the app just never asks for them unless
    # SOURCES includes flux. podinfo (helmreleases.json) and network-policy-controller
    # (in deployments.json, via a kustomize.toolkit.fluxcd.io/name label) exist to prove
    # a Flux-managed workload lands in the service table, not "not managed by ArgoCD".
    cp testdata/helmreleases.json   "$FAKE_DIR/apis/helm.toolkit.fluxcd.io/v2/helmreleases"
    cp testdata/kustomizations.json "$FAKE_DIR/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations"
    # The discovery document and the object collection both live under
    # .../security.istio.io/v1, so the discovery doc is served as index.html: a GET
    # with no trailing slash 301s to the directory, which python's http.server then
    # answers from index.html. Go's http.Client follows that redirect transparently.
    cp testdata/istio-discovery.json "$FAKE_DIR/apis/security.istio.io/v1/index.html"
    cp testdata/peerauthentication.json "$FAKE_DIR/apis/security.istio.io/v1/namespaces/istio-system/peerauthentications/default"
    # Cluster-wide collection endpoint (list), a plain file alongside index.html and
    # the by-name object above, same trick deployments/statefulsets/daemonsets below
    # already use: MeshPolicy's single-object-by-name read is unaffected either way.
    # Exercises all three PeerAuthentication precedence levels: mesh-wide STRICT
    # (istio-system), namespace-wide PERMISSIVE (search-api), and a workload-scoped
    # DISABLE (admin-ui, matched on the "app: admin-ui" pod label added below).
    cp testdata/peerauthentications.json "$FAKE_DIR/apis/security.istio.io/v1/peerauthentications"
    # --directory instead of a `cd X && python3 ...` subshell: a subshell's PID is
    # what $! captures, not python's own -- cleanup()'s kill on Ctrl-C then only
    # signals the subshell wrapper, and python survives as an orphaned grandchild
    # still holding the port. --directory (Python 3.7+) needs no subshell at all, so
    # $! is python's real PID and cleanup's kill actually reaches it.
    python3 -m http.server "$PROXY_PORT" --directory "$FAKE_DIR" >/dev/null 2>&1 &
    PIDS+=($!)
    API="http://127.0.0.1:$PROXY_PORT"
    ROOT_APP="${ROOT_APP_NAME:-root-app}"   # the fixture's root app is named root-app
    NODE_STATS_DEFAULT=true
    ENV_NAME_DEFAULT=local
    UNMANAGED_DEFAULT=true
    MESH_MTLS_DEFAULT=true
    echo "mode: fixture (offline, synthetic data)"
    ;;
  cluster)
    if [[ -z "$CONTEXT" ]]; then
      usage
    fi
    echo "mode: cluster — context '$CONTEXT'"
    # Only require the ArgoCD CRD when ArgoCD is actually one of the sources;
    # a Flux-only cluster has no Applications and must still work.
    case "${SOURCES:-argocd}" in
      *argocd*|auto)
        # Captured, not swallowed: kubectl failing here is usually expired auth (SSO
        # session, stale kubeconfig token), not "this cluster has no ArgoCD" -- surface
        # its own error instead of asserting a conclusion it never actually checked.
        if ! app_check=$(kubectl --context "$CONTEXT" get applications.argoproj.io -n argocd 2>&1); then
          if [ "${SOURCES:-argocd}" = "auto" ]; then
            echo "note: could not read ArgoCD Applications in '$CONTEXT'; relying on auto-detection. kubectl said:" >&2
            echo "$app_check" >&2
          else
            echo "error: could not read ArgoCD Applications in '$CONTEXT' -- this may mean the cluster" >&2
            echo "has none (try SOURCES=flux), or that kubectl itself could not reach it (expired SSO," >&2
            echo "stale kubeconfig). kubectl said:" >&2
            echo "$app_check" >&2
            exit 1
          fi
        fi
        ;;
    esac
    kubectl --context "$CONTEXT" proxy --port="$PROXY_PORT" >/dev/null 2>&1 &
    PIDS+=($!)
    API="http://127.0.0.1:$PROXY_PORT"
    ROOT_APP="${ROOT_APP_NAME:-}"
    # Your kubeconfig can already read nodes and workloads, so show both by default locally.
    NODE_STATS_DEFAULT=true
    UNMANAGED_DEFAULT=true
    # Degrades to "no service mesh detected" on a cluster without Istio, so it is safe
    # to default on here too.
    MESH_MTLS_DEFAULT=true
    # Show which cluster you are actually pointed at, not a generic label.
    ENV_NAME_DEFAULT="$CONTEXT"
    ;;
  *) usage ;;
esac

sleep 2
KUBE_API_URL="$API" \
ENV_NAME="${ENV_NAME:-$ENV_NAME_DEFAULT}" ENV_TYPE="${ENV_TYPE:-}" REGION="${REGION:-}" \
CLUSTER_NAME="${CLUSTER_NAME:-}" ROOT_APP_NAME="$ROOT_APP" \
ARGOCD_UI_BASE="${ARGOCD_UI_BASE:-}" \
IGNORE_GLOBS="${IGNORE_GLOBS:-}" NODE_STATS="${NODE_STATS:-$NODE_STATS_DEFAULT}" \
PENDING_REASONS="${PENDING_REASONS:-$UNMANAGED_DEFAULT}" UNMANAGED="${UNMANAGED:-$UNMANAGED_DEFAULT}" UNMANAGED_IGNORE_NS="${UNMANAGED_IGNORE_NS:-}" \
MESH_MTLS="${MESH_MTLS:-$MESH_MTLS_DEFAULT}" MESH_NAMESPACE="${MESH_NAMESPACE:-istio-system}" \
AZ_SPREAD="${AZ_SPREAD:-$UNMANAGED_DEFAULT}" \
PORT="$PORT" \
/tmp/k8s-status-local &
PIDS+=($!)

echo
echo "  page   http://127.0.0.1:$PORT/k8s-status/"
echo "  json   http://127.0.0.1:$PORT/k8s-status/api/status"
echo "  health http://127.0.0.1:$PORT/k8s-status/healthz"
echo

# /healthz answers the instant the process starts listening, before the collector's
# first real fetch -- it says nothing about /api/status being ready yet. A fixed sleep
# here used to be enough, but MESH_MTLS added another network round trip to that first
# fetch, so against a real (especially remote) cluster it can still be mid-flight,
# which showed up as a raw python traceback instead of a useful message. Poll the
# actual endpoint instead of guessing how long it needs.
summary=""
for _ in $(seq 1 30); do
  # set -e is active: curl's normal "connection refused" exit while the server is
  # still starting must not kill the script (and, via the EXIT trap, the server it
  # just started) on the very first failed poll -- `|| true` makes an expected
  # failure here just an empty $summary, not a script-ending error.
  summary="$(curl -s "http://127.0.0.1:$PORT/k8s-status/api/status" || true)"
  if [ -n "$summary" ] && printf '%s' "$summary" | python3 -c 'import json,sys; json.load(sys.stdin)' 2>/dev/null; then
    break
  fi
  summary=""
  sleep 1
done
if [ -n "$summary" ]; then
  printf '%s' "$summary" | python3 -c 'import json,sys;d=json.load(sys.stdin);print("summary:",d["summary"]);print("error:",d["error"])'
else
  echo "(still starting up -- the page above will work once the first fetch completes)"
fi
echo
echo "Ctrl-C to stop."
wait

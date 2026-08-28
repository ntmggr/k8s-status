#!/usr/bin/env bash
# Run k8s-status locally. Two modes:
#   ./scripts/local-test.sh fixture             offline, uses testdata/applications.json
#   ./scripts/local-test.sh cluster <context>   live, via kubectl proxy (kube context is required)
#
# Deployment-specific values are not baked in. Override ENV_NAME, ENV_TYPE, REGION,
# CLUSTER_NAME, ARGOCD_UI_BASE, ROOT_APP_NAME, IGNORE_GLOBS, NODE_STATS, UNMANAGED,
# UNMANAGED_IGNORE_NS and AZ_SPREAD in the environment as needed.
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

cd "$ROOT"
go build -o /tmp/k8s-status-local ./cmd/k8s-status

case "$MODE" in
  fixture)
    FAKE_DIR="$(mktemp -d)"
    mkdir -p "$FAKE_DIR/apis/argoproj.io/v1alpha1/namespaces/argocd"
    mkdir -p "$FAKE_DIR/apis/apps/v1" "$FAKE_DIR/api/v1"
    cp testdata/applications.json "$FAKE_DIR/apis/argoproj.io/v1alpha1/namespaces/argocd/applications"
    cp testdata/nodes.json        "$FAKE_DIR/api/v1/nodes"
    cp testdata/pods.json         "$FAKE_DIR/api/v1/pods"
    cp testdata/deployments.json  "$FAKE_DIR/apis/apps/v1/deployments"
    cp testdata/statefulsets.json "$FAKE_DIR/apis/apps/v1/statefulsets"
    cp testdata/daemonsets.json   "$FAKE_DIR/apis/apps/v1/daemonsets"
    ( cd "$FAKE_DIR" && python3 -m http.server "$PROXY_PORT" >/dev/null 2>&1 ) &
    PIDS+=($!)
    API="http://127.0.0.1:$PROXY_PORT"
    ROOT_APP="${ROOT_APP_NAME:-root-app}"   # the fixture's root app is named root-app
    NODE_STATS_DEFAULT=true
    ENV_NAME_DEFAULT=local
    UNMANAGED_DEFAULT=true
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
AZ_SPREAD="${AZ_SPREAD:-$UNMANAGED_DEFAULT}" \
PORT="$PORT" \
/tmp/k8s-status-local &
PIDS+=($!)

sleep 2
echo
echo "  page   http://127.0.0.1:$PORT/k8s-status/"
echo "  json   http://127.0.0.1:$PORT/k8s-status/api/status"
echo "  health http://127.0.0.1:$PORT/k8s-status/healthz"
echo
curl -s "http://127.0.0.1:$PORT/k8s-status/api/status" \
  | python3 -c 'import json,sys;d=json.load(sys.stdin);print("summary:",d["summary"]);print("error:",d["error"])' || true
echo
echo "Ctrl-C to stop."
wait

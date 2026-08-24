#!/usr/bin/env bash
# Run srv-status locally. Two modes:
#   ./scripts/local-test.sh fixture             offline, uses testdata/applications.json
#   ./scripts/local-test.sh cluster <context>   live, via kubectl proxy (kube context is required)
#
# Deployment-specific values are not baked in. Override ENV_NAME, ENV_TYPE, REGION,
# CLUSTER_NAME, ARGOCD_UI_BASE, ROOT_APP_NAME, IGNORE_GLOBS and NODE_STATS in the
# environment as needed.
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
go build -o /tmp/srv-status-local ./cmd/srv-status

case "$MODE" in
  fixture)
    FAKE_DIR="$(mktemp -d)"
    mkdir -p "$FAKE_DIR/apis/argoproj.io/v1alpha1/namespaces/argocd"
    cp testdata/applications.json "$FAKE_DIR/apis/argoproj.io/v1alpha1/namespaces/argocd/applications"
    ( cd "$FAKE_DIR" && python3 -m http.server "$PROXY_PORT" >/dev/null 2>&1 ) &
    PIDS+=($!)
    API="http://127.0.0.1:$PROXY_PORT"
    ROOT_APP="${ROOT_APP_NAME:-root-app}"   # the fixture's root app is named root-app
    echo "mode: fixture (offline, synthetic data)"
    ;;
  cluster)
    if [[ -z "$CONTEXT" ]]; then
      usage
    fi
    echo "mode: cluster — context '$CONTEXT'"
    kubectl --context "$CONTEXT" get applications.argoproj.io -n argocd >/dev/null
    kubectl --context "$CONTEXT" proxy --port="$PROXY_PORT" >/dev/null 2>&1 &
    PIDS+=($!)
    API="http://127.0.0.1:$PROXY_PORT"
    ROOT_APP="${ROOT_APP_NAME:-ocp-services}"
    # Show which cluster you are actually pointed at, not a generic label.
    CLUSTER_NAME="${CLUSTER_NAME:-$CONTEXT}"
    ;;
  *) usage ;;
esac

sleep 2
KUBE_API_URL="$API" \
ENV_NAME="${ENV_NAME:-local}" ENV_TYPE="${ENV_TYPE:-}" REGION="${REGION:-}" \
CLUSTER_NAME="${CLUSTER_NAME:-}" ROOT_APP_NAME="$ROOT_APP" \
ARGOCD_UI_BASE="${ARGOCD_UI_BASE:-}" \
IGNORE_GLOBS="${IGNORE_GLOBS:-}" NODE_STATS="${NODE_STATS:-false}" PORT="$PORT" \
/tmp/srv-status-local &
PIDS+=($!)

sleep 2
echo
echo "  page   http://127.0.0.1:$PORT/srv-status/"
echo "  json   http://127.0.0.1:$PORT/srv-status/api/status"
echo "  health http://127.0.0.1:$PORT/srv-status/healthz"
echo
curl -s "http://127.0.0.1:$PORT/srv-status/api/status" \
  | python3 -c 'import json,sys;d=json.load(sys.stdin);print("summary:",d["summary"]);print("error:",d["error"])' || true
echo
echo "Ctrl-C to stop."
wait

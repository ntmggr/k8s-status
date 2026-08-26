#!/usr/bin/env bash
# One-command local demo: throwaway kind cluster, fake ArgoCD Applications, real tool.
#
#   hack/demo.sh              create the cluster (if needed), seed it, serve the page
#   hack/demo.sh --cleanup    delete the cluster
#
# Nothing outside the kind cluster is touched, and only the cluster named below is
# ever deleted. See docs/demo.md.
set -euo pipefail

CLUSTER="k8s-status-demo"
CONTEXT="kind-${CLUSTER}"
NAMESPACE="argocd"
ROOT_APP="platform"              # any name works: the app-of-apps is detected by shape
PORT="${PORT:-8080}"
PROXY_PORT="${PROXY_PORT:-8001}"

HACK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$HACK_DIR/.." && pwd)"
BIN_DIR=""
PIDS=()

usage() {
  cat >&2 <<USAGE
usage: $0 [--cleanup]

  (no args)   create the '$CLUSTER' kind cluster if missing, apply the sample
              Applications, and serve the status page on port $PORT
  --cleanup   delete the '$CLUSTER' kind cluster and exit

env overrides: PORT (default 8080), PROXY_PORT (default 8001)
USAGE
  exit 2
}

log() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

cleanup_processes() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  [[ -n "$BIN_DIR" ]] && rm -rf "$BIN_DIR"
  return 0
}

require_tools() {
  local missing=()
  for t in kind kubectl docker go; do
    command -v "$t" >/dev/null 2>&1 || missing+=("$t")
  done
  if ((${#missing[@]})); then
    die "missing required tool(s): ${missing[*]} — install them and re-run"
  fi
  docker info >/dev/null 2>&1 || die "docker is installed but not running — start Docker and re-run"
}

delete_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    log "deleting kind cluster '$CLUSTER'"
    kind delete cluster --name "$CLUSTER"
  else
    log "kind cluster '$CLUSTER' does not exist; nothing to delete"
  fi
}

create_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    log "kind cluster '$CLUSTER' already exists; reusing it"
  else
    log "creating kind cluster '$CLUSTER' (a minute or so on first run)"
    kind create cluster --name "$CLUSTER" --wait 120s
  fi
  kubectl --context "$CONTEXT" cluster-info >/dev/null
}

seed_cluster() {
  # Only the Application CRD, not ArgoCD itself: the tool reads Application objects
  # and nothing else, and a full ArgoCD install would add minutes for no extra signal.
  log "installing the Application CRD"
  kubectl --context "$CONTEXT" apply -f "$HACK_DIR/application-crd.yaml"
  kubectl --context "$CONTEXT" wait --for=condition=Established \
    crd/applications.argoproj.io --timeout=60s >/dev/null

  log "applying sample Applications"
  kubectl --context "$CONTEXT" apply -f "$HACK_DIR/demo-apps.yaml"
}

# The sample objects carry their status inline, which only persists because the
# vendored CRD declares no status subresource. Read it back rather than assume:
# a silently dropped status would turn every row into a WARNING.
verify_status() {
  log "verifying status was persisted"
  local apps missing=()
  apps="$(kubectl --context "$CONTEXT" -n "$NAMESPACE" get applications.argoproj.io \
    -o jsonpath='{range .items[*]}{.metadata.name}={.status.health.status}{"\n"}{end}')"
  while IFS='=' read -r name health; do
    [[ -z "$name" ]] && continue
    [[ -z "$health" ]] && missing+=("$name")
  done <<<"$apps"
  if ((${#missing[@]})); then
    die "status was dropped on: ${missing[*]} — is a status subresource enabled on the CRD?"
  fi
  local prunes
  prunes="$(kubectl --context "$CONTEXT" -n "$NAMESPACE" get applications.argoproj.io "$ROOT_APP" \
    -o jsonpath='{.status.resources[?(@.requiresPruning==true)].name}')"
  [[ -n "$prunes" ]] || die "root app '$ROOT_APP' has no requiresPruning entry; the PRUNE row would not render"
  printf '    %s\n' "$apps" | sed 's/^ *//'
  log "root app '$ROOT_APP' flags for pruning: $prunes"
}

serve() {
  log "building k8s-status"
  BIN_DIR="$(mktemp -d)"
  ( cd "$ROOT_DIR" && go build -ldflags "-X main.version=demo" -o "$BIN_DIR/k8s-status" ./cmd/k8s-status )

  # kubectl proxy authenticates as the current kubeconfig user, so the binary needs
  # no ServiceAccount token. Same trick as scripts/local-test.sh cluster mode.
  log "starting kubectl proxy on :$PROXY_PORT"
  kubectl --context "$CONTEXT" proxy --port="$PROXY_PORT" >/dev/null 2>&1 &
  PIDS+=($!)
  for _ in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:$PROXY_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.5
  done

  log "starting k8s-status on :$PORT"
  KUBE_API_URL="http://127.0.0.1:$PROXY_PORT" \
  ENV_NAME="k8s-status-demo" ENV_TYPE="dev" REGION="local" \
  CLUSTER_NAME="$CLUSTER" ARGOCD_NAMESPACE="$NAMESPACE" ROOT_APP_NAME="$ROOT_APP" \
  BASE_PATH="/k8s-status" PORT="$PORT" \
  NODE_STATS="${NODE_STATS:-true}" UNMANAGED="${UNMANAGED:-true}" \
  "$BIN_DIR/k8s-status" &
  PIDS+=($!)

  for _ in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:$PORT/k8s-status/healthz" >/dev/null 2>&1 && break
    sleep 0.5
  done

  echo
  echo "  page   http://127.0.0.1:$PORT/k8s-status/"
  echo "  json   http://127.0.0.1:$PORT/k8s-status/api/status"
  echo "  health http://127.0.0.1:$PORT/k8s-status/healthz"
  echo
  curl -fsS "http://127.0.0.1:$PORT/k8s-status/api/status" | python3 -c '
import json, sys
d = json.load(sys.stdin)
print("summary:", json.dumps(d["summary"]))
print("error:  ", d["error"])
for s in d["services"]:
    print("  %-16s %-12s %-10s %s" % (s["name"], s["state"], s.get("appVersion") or "-", s["sync"]))
' || true
  echo
  echo "Ctrl-C to stop. The cluster survives; remove it with: $0 --cleanup"
  wait
}

main() {
  case "${1:-}" in
    --cleanup) require_tools; delete_cluster; exit 0 ;;
    -h|--help) usage ;;
    "") ;;
    *) usage ;;
  esac

  trap cleanup_processes EXIT INT TERM
  require_tools
  create_cluster
  seed_cluster
  verify_status
  serve
}

main "$@"

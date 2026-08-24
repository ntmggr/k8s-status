#!/usr/bin/env bash
# Install srv-status into a cluster.
#
#   ./scripts/install.sh          <kube-context> <image> [env-name]   # raw manifest
#   ./scripts/install.sh --helm   <kube-context> <image> [env-name]   # Helm chart
#
# The plain-manifest path needs nothing but kubectl and is the no-Helm escape hatch.
# Everything is read-only; the only thing it can see is ArgoCD Applications.
set -euo pipefail

MODE="manifest"
if [[ "${1:-}" == "--helm" ]]; then
  MODE="helm"
  shift
elif [[ "${1:-}" == "--manifest" ]]; then
  shift
fi

CONTEXT="${1:-}"
IMAGE="${2:-}"
ENV_NAME="${3:-$CONTEXT}"
NAMESPACE="srv-status"
RELEASE="srv-status"

if [[ -z "$CONTEXT" || -z "$IMAGE" ]]; then
  echo "usage: $0 [--helm|--manifest] <kube-context> <image> [env-name]" >&2
  echo "   eg: $0 k8s-dev registry.example.com/srv-status:0.1.0" >&2
  echo "   eg: $0 --helm k8s-dev registry.example.com/srv-status:0.1.0 dev" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$ROOT/charts/srv-status"

# Fail early with a clear message rather than a half-applied install.
kubectl --context "$CONTEXT" get namespace argocd >/dev/null
kubectl --context "$CONTEXT" get crd applications.argoproj.io >/dev/null

if [[ "$MODE" == "helm" ]]; then
  command -v helm >/dev/null || { echo "helm not found; rerun without --helm" >&2; exit 2; }

  # Split "repo:tag" on the last colon so registries with a port still work.
  IMAGE_REPO="${IMAGE%:*}"
  IMAGE_TAG="${IMAGE##*:}"
  if [[ "$IMAGE_REPO" == "$IMAGE" ]]; then
    echo "image must include a tag, eg registry.example.com/srv-status:0.1.0" >&2
    exit 2
  fi

  helm --kube-context "$CONTEXT" upgrade --install "$RELEASE" "$CHART" \
    --namespace "$NAMESPACE" --create-namespace \
    --set image.repository="$IMAGE_REPO" \
    --set image.tag="$IMAGE_TAG" \
    --set config.envName="$ENV_NAME" \
    --set config.clusterName="$CONTEXT" \
    --wait --timeout 90s

  UNINSTALL="helm --kube-context ${CONTEXT} uninstall ${RELEASE} -n ${NAMESPACE}"
else
  sed -e "s|__IMAGE__|${IMAGE}|g" \
      -e "s|__ENV_NAME__|${ENV_NAME}|g" \
      -e "s|__CLUSTER_NAME__|${CONTEXT}|g" \
      "$ROOT/deploy/install.yaml" \
    | kubectl --context "$CONTEXT" apply -f -

  kubectl --context "$CONTEXT" -n "$NAMESPACE" rollout status deploy/srv-status --timeout=90s

  UNINSTALL="kubectl --context ${CONTEXT} delete -f ${ROOT}/deploy/install.yaml --ignore-not-found"
fi

cat <<EOF

Installed on '${CONTEXT}' via ${MODE}. To view it:

  kubectl --context ${CONTEXT} -n ${NAMESPACE} port-forward svc/srv-status 8080:80
  open http://127.0.0.1:8080/srv-status/

To remove it:

  ${UNINSTALL}
EOF

# k8s-status Helm chart

Installs `k8s-status`, a read-only HTML status page that renders ArgoCD `Application`
health for the cluster it runs in. It is a long-running extra service: one replica, no
database, no persistent storage, and no outbound calls except the in-cluster Kubernetes
API.

## What it deploys

| Object | Namespace | Notes |
| --- | --- | --- |
| `ServiceAccount` | release namespace | No annotations. Created when `serviceAccount.create` is true. |
| `Role` | `rbac.argocdNamespace` | `get`/`list` on `argoproj.io/applications`. |
| `RoleBinding` | `rbac.argocdNamespace` | Binds the Role to the ServiceAccount in the release namespace. |
| `ClusterRole` | cluster-scoped | Optional, disabled by default. `get`/`list` on `nodes` for `config.nodeStats`, plus `get`/`list` on `apps` workloads when `config.unmanaged` is on. |
| `ClusterRoleBinding` | cluster-scoped | Optional, disabled by default. Binds that ClusterRole to the ServiceAccount. |
| `ConfigMap` | release namespace | Every `config.*` value as a quoted string; consumed via `envFrom`. |
| `Deployment` | release namespace | One replica, distroless, non-root, read-only root filesystem. |
| `Service` | release namespace | `ClusterIP`, port 80 to container port 8080. |
| `Ingress` | release namespace | Optional, disabled by default. |
| `VirtualService` | release namespace | Optional Istio route, disabled by default. |

There is no Job, CronJob, PVC, HPA, PDB, Secret or SecretProviderClass. That is
deliberate: the whole chart is meant to be readable end to end in a couple of minutes.

## Base path — no rewrite

The service serves everything under `config.basePath` (`/k8s-status`) natively. Both the
Ingress and the VirtualService forward the path unchanged; there is **no** rewrite
annotation and no Istio `rewrite:` block. If you add a proxy in front of this, do not
strip the prefix.

Health, liveness and readiness all use `{{ basePath }}/healthz`, which does not depend on
the Kubernetes API and stays healthy while the ArgoCD read is failing.

## RBAC rationale

The chart creates a namespaced `Role` scoped to the namespace holding the ArgoCD
`Application` resources, rather than binding the built-in `view` ClusterRole:

- `view` is cluster-wide and covers ConfigMaps, Services, Pods, and every other readable
  resource in every namespace. `k8s-status` needs one resource type in one namespace.
- The blast radius of this chart is then provable by reading eight lines of YAML: if the
  process were ever compromised, the token can list ArgoCD Application objects and
  nothing else.
- There is no `watch` verb. The collector polls on a TTL cache rather than maintaining an
  informer, so `watch` would be unused permission.

Set `rbac.create: false` if your platform manages RBAC separately; the Deployment will
still reference the ServiceAccount.

### The one optional cluster-scoped grant

Two sections need reads a namespaced Role cannot serve, so both they and the permission
are off by default and the default render contains no cluster-scoped object at all:

```sh
helm template k8s-status charts/k8s-status | grep -c "kind: ClusterRole"   # 0
```

| Value | Adds to the ClusterRole | Why it cannot be a Role |
| --- | --- | --- |
| `config.nodeStats` | `get`/`list` on `nodes` | Nodes are cluster-scoped. |
| `config.unmanaged` | `get`/`list` on `deployments`, `statefulsets`, `daemonsets` in `apps` | The list spans every namespace. |

Each rule is gated on its own feature, so turning one on does not grant the other.
`rbac.clusterRole=true` alone renders only the `nodes` rule.

Enable a feature and its permission together:

```sh
helm upgrade --install k8s-status charts/k8s-status \
  --set config.nodeStats=true --set config.unmanaged=true --set rbac.clusterRole=true
```

Setting either `config.*` flag without `rbac.clusterRole=true` is safe: the read is denied
and that section renders a note saying so, while the rest of the page is unaffected.

## No IRSA role

`k8s-status` makes zero AWS API calls. It reads the projected ServiceAccount token and
the cluster CA from the pod filesystem and talks only to the in-cluster Kubernetes API
endpoint. `serviceAccount.annotations` is therefore empty by default, with no
`eks.amazonaws.com/role-arn`. Adding one would hand the pod AWS credentials it has no use
for. Leave it empty.

## Values

| Key | Default | Description |
| --- | --- | --- |
| `replicaCount` | `1` | Number of pods. Stateless, but one is enough. |
| `image.repository` | `docker.io/ntmggr/k8s-status` | Container image repository. |
| `image.tag` | `""` | Image tag; empty falls back to `.Chart.AppVersion`. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Secrets for pulling from a private registry. |
| `nameOverride` | `""` | Override the chart name used in resource names. |
| `fullnameOverride` | `""` | Override the full generated resource name. |
| `serviceAccount.create` | `true` | Create the ServiceAccount. |
| `serviceAccount.name` | `""` | ServiceAccount name; empty uses the generated fullname. |
| `serviceAccount.annotations` | `{}` | Deliberately empty — no IRSA role needed. |
| `rbac.create` | `true` | Create the namespaced Role and RoleBinding. |
| `rbac.argocdNamespace` | `argocd` | Namespace the Role and RoleBinding are created in. |
| `rbac.clusterRole` | `false` | Create the ClusterRole and ClusterRoleBinding. Needed by `config.nodeStats` and `config.unmanaged`; each contributes only its own rule. |
| `config.envName` | `unknown` | `ENV_NAME` — environment name in the page header. |
| `config.envType` | `""` | `ENV_TYPE` — environment class, e.g. dev / stage / prod. |
| `config.region` | `""` | `REGION` — cloud region in the page header. |
| `config.clusterName` | `""` | `CLUSTER_NAME` — cluster name in the page header. |
| `config.basePath` | `/k8s-status` | `BASE_PATH` — path prefix served natively, no rewrite. |
| `config.argocdNamespace` | `argocd` | `ARGOCD_NAMESPACE` — namespace to read Applications from. |
| `config.rootAppName` | `""` | `ROOT_APP_NAME` — app-of-apps used to derive prune orphans. Empty detects it. |
| `config.argocdUIBase` | `""` | `ARGOCD_UI_BASE` — ArgoCD UI base URL for deep links. |
| `config.ignoreGlobs` | `""` | `IGNORE_GLOBS` — comma-separated globs of Application names to hide. |
| `config.cacheTTLSeconds` | `15` | `CACHE_TTL_SECONDS` — snapshot cache TTL. |
| `config.refreshSeconds` | `30` | `REFRESH_SECONDS` — page meta-refresh interval. |
| `config.nodeStats` | `false` | `NODE_STATS` — render the cluster capacity section. Requires `rbac.clusterRole`. |
| `config.unmanaged` | `false` | `UNMANAGED` — list workloads ArgoCD does not manage. Requires `rbac.clusterRole`. |
| `config.unmanagedIgnoreNamespaces` | `""` | `UNMANAGED_IGNORE_NS` — comma-separated namespace globs excluded from that list. |
| `config.port` | `8080` | `PORT` — container listen port. |
| `service.type` | `ClusterIP` | Service type. |
| `service.port` | `80` | Service port. |
| `ingress.enabled` | `false` | Create an Ingress. |
| `ingress.className` | `""` | `ingressClassName`. |
| `ingress.annotations` | `{}` | Extra Ingress annotations. |
| `ingress.host` | `""` | Hostname; required when `ingress.enabled` is true. |
| `ingress.tls` | `[]` | TLS blocks passed straight to the Ingress spec. |
| `istio.enabled` | `false` | Create an Istio VirtualService. |
| `istio.gateway` | `[]` | Gateways to attach the VirtualService to. |
| `istio.hosts` | `[]` | Hosts served by the VirtualService; empty renders `"*"`. |
| `resources.requests.cpu` | `50m` | CPU request. |
| `resources.requests.memory` | `64Mi` | Memory request. |
| `resources.limits.memory` | `128Mi` | Memory limit. No CPU limit, to avoid throttling. |
| `nodeSelector` | `{}` | Node selector. |
| `tolerations` | `[]` | Tolerations. |
| `affinity` | `{}` | Affinity rules. |
| `podAnnotations` | `{}` | Extra pod annotations. |
| `podLabels` | `{}` | Extra pod labels. |

`config.*` maps 1:1 to the environment variables read by the binary. A change to any of
them updates the ConfigMap, which changes the `checksum/config` pod annotation and rolls
the Deployment.

## Install with Helm

```sh
helm upgrade --install k8s-status charts/k8s-status \
  --namespace k8s-status --create-namespace \
  --set image.repository=<registry>/k8s-status \
  --set image.tag=0.1.0 \
  --set config.envName=<env> \
  --set config.clusterName=<cluster>
```

The repo ships `scripts/install.sh --helm`, which wraps the same call with a kube
context:

```sh
./scripts/install.sh --helm <kube-context> <image> [env-name]
```

Behind an ingress:

```sh
helm upgrade --install k8s-status charts/k8s-status \
  --namespace k8s-status --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.host=status.example.com
```

Uninstall:

```sh
helm uninstall k8s-status --namespace k8s-status
```

## Install with ArgoCD

Point an `Application` at the chart path in this repo. The chart holds no cluster-specific
values, so per-cluster settings go in `helm.values`.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: k8s-status
  namespace: argocd
spec:
  project: default
  source:
    repoURL: <this-repo-url>
    targetRevision: main
    path: charts/k8s-status
    helm:
      values: |
        image:
          repository: <registry>/k8s-status
          tag: "0.1.0"
        config:
          envName: <env>
          clusterName: <cluster>
          region: <region>
  destination:
    server: https://kubernetes.default.svc
    namespace: k8s-status
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
```

The Role and RoleBinding land in `rbac.argocdNamespace`, not in the destination
namespace. If your ArgoCD project restricts namespaces, allow both.

## Without Helm

`deploy/install.yaml` at the repo root is a dependency-free equivalent applied with
`kubectl apply -f`. Use it when Helm is not available; it is not generated from this
chart and the two are maintained side by side.

## Validate changes

```sh
helm lint charts/k8s-status
helm template k8s-status charts/k8s-status
helm template k8s-status charts/k8s-status | kubectl apply --dry-run=client --validate=strict -f -
```

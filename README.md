# srv-status

A read-only, single-page status view of one OCP environment.

`srv-status` runs inside a Kubernetes cluster, reads ArgoCD `Application` objects from that
cluster's API server, and renders one HTML page showing the environment's deployed version plus a
table of every OCP service and whether it is healthy. It reports on its own cluster only — there is
no peer discovery, no fan-out, and no outbound network call other than to the local Kubernetes API.

Standard library only. No client-go, no web framework, no JavaScript, no external assets.

## Routes

All routes are served under `BASE_PATH` (default `/srv-status`).

| Route | Response |
|---|---|
| `GET <base>/` | HTML status page; accepts the [filter and refresh parameters](#filtering-and-refresh) |
| `GET <base>/api/status` | JSON, **always HTTP 200** — see below; accepts the same filter parameters |
| `GET <base>/healthz` | `200 ok` |
| `GET <base>` | `302` to `<base>/` |

`/api/status` always returns 200 so a scraper can tell "the app is up but the cluster read failed"
(`"error"` non-null, `"services": []`) apart from "the app is down" (connection refused / 5xx).

`/healthz` is the liveness probe and deliberately does **not** touch the Kubernetes API. A broken
ArgoCD or a revoked token must not get the pod restarted — restarting fixes neither.

### JSON shape

```json
{"schema":1,"env":"sample-dev","envType":"dev","region":"eu-west-1",
 "clusterName":"sample-dev-cluster","clusterPath":"sample-dev-cluster",
 "version":"develop","revision":"437a162...","rootHealth":"Degraded","rootSync":"OutOfSync",
 "phase":"Succeeded","message":"successfully synced (all tasks run)",
 "lastDeployedAt":"2026-08-21T11:30:57Z","lastDeployId":1048,
 "summary":{"total":151,"ok":143,"degraded":3,"progressing":2,"prune":9,"suspended":0,"hidden":0},
 "services":[{"name":"accounts-api","version":"develop","revision":"3c9646b...",
              "state":"DEGRADED","sync":"OutOfSync","health":"Degraded",
              "detail":"0/2 replicas available"}],
 "checkedAt":"2026-08-24T09:10:11Z","ageSeconds":4,"stale":false,"error":null}
```

`detail` and `message` are truncated to 200 characters.

The `nodes` object is present only when `NODE_STATS=true`:

```json
{"nodes":{"total":82,"cpuNodes":74,"gpuNodes":8,"gpus":14,"gpuServices":47,
          "arch":{"amd64":16,"arm64":66}}}
```

## Quick start

Install on any cluster that runs ArgoCD. Read-only throughout — the ServiceAccount can only
`get`/`list` ArgoCD `Application` objects in a single namespace, and there is no ClusterRole.

```bash
./scripts/install.sh <kube-context> <image> [env-name]
```

Then view it without needing DNS, an ingress or a load balancer:

```bash
kubectl --context <kube-context> -n srv-status port-forward svc/srv-status 8080:80
open http://127.0.0.1:8080/srv-status/
```

Uninstall is one command:

```bash
kubectl --context <kube-context> delete -f deploy/install.yaml --ignore-not-found
```

`deploy/install.yaml` is a single self-contained manifest (Namespace, ServiceAccount, Role,
RoleBinding, ConfigMap, Deployment, Service). No Helm, no ArgoCD and no cluster-scoped
permissions are required to run it — the script only substitutes the image, environment name
and cluster name before applying. Tune behaviour afterwards by editing the `srv-status`
ConfigMap and restarting the deployment; every key is listed in [Configuration](#configuration).

To try it with no cluster at all, see [Running locally](#running-locally).

## Configuration

Everything is read from the environment at startup. Every variable has a default.

| Variable | Default | Meaning |
|---|---|---|
| `ENV_NAME` | `unknown` | Display name, e.g. `sample-dev` |
| `ENV_TYPE` | *(empty)* | `dev` / `stage` / `prod`; falls back to the root app's `EnvType` label |
| `CLUSTER_NAME` | *(empty)* | Cluster identifier shown in the footer and JSON |
| `REGION` | *(empty)* | e.g. `eu-west-1` |
| `BASE_PATH` | `/srv-status` | Path prefix served natively; normalized to a leading slash with no trailing slash. Empty means root |
| `ARGOCD_NAMESPACE` | `argocd` | Namespace holding the `Application` objects |
| `ROOT_APP_NAME` | `ocp-services` | App-of-apps that owns every service |
| `ARGOCD_UI_BASE` | *(empty)* | When set, service names link to `<base>/applications/<name>` |
| `IGNORE_GLOBS` | *(empty)* | Comma-separated `path.Match` globs; matches are hidden from the table and all counts, and reported as `hidden` |
| `NODE_STATS` | `false` | Render the cluster capacity section. Needs a ClusterRole for `nodes` — see [Cluster capacity](#cluster-capacity) |
| `CACHE_TTL_SECONDS` | `15` | Snapshot cache lifetime |
| `REFRESH_SECONDS` | `30` | Default `<meta http-equiv="refresh">` interval; a viewer can override it per request with `?refresh=` |
| `PORT` | `8080` | Listen port |
| `KUBERNETES_SERVICE_HOST` | *(injected)* | Used to build the API server URL |
| `KUBERNETES_SERVICE_PORT` | `443` | Used to build the API server URL |
| `KUBE_API_URL` | *(empty)* | Local development only — overrides the in-cluster API URL (see below) |
| `KUBE_TOKEN_FILE` | `/dev/null` | Local development only — token file used with `KUBE_API_URL` |

Nothing is read from a config file, and the service account token is never logged.

## State rules

Applications are split into the root app (`metadata.name == ROOT_APP_NAME`) and its children.
The root's `status.resources[]` entries carry a `requiresPruning` flag; that map, keyed by resource
name, is the only source of prune information. `rootSettled` means the root's
`status.operationState.phase` is not `Running`. An empty `health.status` is normalized to `Unknown`.

Each child is evaluated against the rules below, **first match wins**:

| # | Condition (first match wins) | State |
|---|---|---|
| 1 | Marked `requiresPruning` by the root app | `PRUNE` |
| 2 | Health is `Degraded` or `Missing` | `DEGRADED` |
| 3 | Health is `Unknown` (or empty) | `WARNING` |
| 4 | Health is `Progressing`, **or** the root sync is still running and the child is `OutOfSync` | `PROGRESSING` |
| 5 | Health is `Suspended` | `SUSPENDED` |
| 6 | Child is `OutOfSync` and the root sync has settled | `DRIFT` |
| 7 | Otherwise | `OK` |

The two states that exist purely to stop the page crying wolf:

**`DRIFT`** — the workload is `Healthy` (its pods pass their readiness and liveness checks) but
the manifests no longer match git. On a live dev cluster this was 28 of 33 rows that an earlier
version painted red. Folding drift into `DEGRADED` makes the page permanently alarming and it
gets ignored within a week, so it is reported separately and the row says so in plain words.

**`PRUNE`** — ArgoCD wants to delete the resource but will not without `prune: true`, so it is
reported `OutOfSync` forever. On the same cluster that was 9 of 12 `OutOfSync` children. It is a
cleanup backlog, not an outage.

Note that rule 4 sits **above** rule 6 deliberately. During a normal sync children legitimately
go `OutOfSync` while `Progressing`; flagging that would alarm on every commit. Rule 6 therefore
only fires on *settled* drift — ArgoCD has finished and the cluster still does not match git.

The root app's own `status.health` is **not** used. It aggregates its children and goes stale:
on one cluster it read `Degraded` with a `lastTransitionTime` a year older than the most recent
successful sync. It is shown in a tooltip for reference and carries no weight.

Rows are sorted by severity — `DEGRADED`, `PROGRESSING`, `PRUNE`, `SUSPENDED`, `OK` — then
alphabetically by name inside each group, so the ordering is stable between refreshes and the
things that need attention are always at the top.

### Why prune orphans are their own state

An Application flagged `requiresPruning` is one that ArgoCD still tracks but that the desired state
no longer declares — usually a service that was removed from the environment's values but whose
Application object was never pruned, because automated pruning is off. Such an app is expected to
look wrong: its source path may be gone, its workloads may be scaled to zero, and its health will
often read `Degraded` or `Missing`.

Folding those into `DEGRADED` would mean a permanent block of red on the page for things nobody
intends to fix, which trains people to ignore red. Prune is therefore checked before health,
so an orphan is reported as an orphan even when it is also unhealthy. `PRUNE` is a housekeeping
signal — "delete this Application" — not an outage signal.

### Why the root app's own health is not trusted

The root app-of-apps aggregates its children, so its health degrades whenever *any* child is
unhealthy, and its sync status goes `OutOfSync` whenever any child's manifest has drifted. In a
large environment that means the root is red almost permanently, and its single health string
cannot tell you whether one non-critical worker is restarting or the whole environment is down.

The root is therefore used only for facts it alone knows: the deployed `targetRevision` and
revision, the `EnvType` label, the sync phase and message, the deploy history, and the prune flags.
Every per-service verdict comes from the child Application itself. Root health and root sync are
still surfaced, but only as a tooltip on the version line — context, not a verdict.

The one place the root's phase does matter is rule 3: while the root is mid-sync, an `OutOfSync`
child is simply one ArgoCD has not reached yet, which is `PROGRESSING`, not a problem. Once the root
has settled, the same child being `OutOfSync` means the sync finished and left it behind, which is
`DEGRADED`.

## Filtering and refresh

Both are query parameters on the page, so they need no JavaScript: the page has no
`<script>` tag at all, and `<meta http-equiv="refresh">` re-requests the current URL
including its query string, so a filter survives every auto-refresh.

| Parameter | Meaning |
|---|---|
| `?status=DEGRADED` | Show only that state. Repeatable (`?status=DEGRADED&status=DRIFT`) and comma-separated (`?status=DEGRADED,DRIFT`) |
| `?sync=OutOfSync` | Same semantics; `Synced` / `OutOfSync` / `Unknown` |
| `?gpu=true` / `?gpu=false` | GPU services only / non-GPU only. Omitted means no GPU filtering |
| `?refresh=<seconds>` | Override `REFRESH_SECONDS` for this view. `0` turns auto-refresh off |

Values within one parameter are OR, parameters are ANDed together, so
`?status=DEGRADED,DRIFT&gpu=true` means "(DEGRADED or DRIFT) and GPU". Matching is
case-insensitive and tolerates surrounding whitespace. A value that matches no state —
`?status=BANANAS` — renders an empty table with the active-filter chips still shown, rather
than a 500 or a silent full listing.

`?refresh=` is clamped: anything non-numeric or negative falls back to `REFRESH_SECONDS`,
non-zero values are held between 5 and 3600 seconds. The snapshot cache already absorbs
most of the load, but nothing should be able to ask for a one-second page loop.

The status tiles are links. Clicking one applies that filter, clicking the active one
clears it, and every link is built from the current query so filters and `?refresh=`
compose in both directions. Active filters appear as chips above the table with an `x`
that removes just that one, plus a `showing N of M` count.

**The tiles keep counting the whole cluster while a filter is active.** Filtering selects
rows, it does not change counts — if the tiles counted only the filtered rows, clicking
`DEGRADED` would make every other tile read `0` and the reader would lose their bearings.
The same holds for `summary` in the JSON; `filters.matched` carries the filtered count.

`/api/status` applies the same filters and echoes them back in a `filters` object, omitted
when nothing is filtered:

```json
{"filters":{"status":["DEGRADED","DRIFT"],"gpu":"true","matched":16}}
```

`/healthz` ignores all of it.

## Cluster capacity

The status tiles are mutually exclusive and sum to the total. GPU is not a status — a GPU
service is already counted in `OK` or `DEGRADED` — so it is not a tile. It is reported in a
separate **cluster capacity** section together with the node counts, which is about
infrastructure rather than service health. The per-row `GPU` chip and the row edge accent
are unaffected.

The section is off by default. With `NODE_STATS=true` the service also reads
`/api/v1/nodes` and reports total nodes, CPU nodes, GPU nodes, total GPUs, GPU services and
the architecture split from `.status.nodeInfo.architecture`.

A node counts as a GPU node when `.status.capacity["nvidia.com/gpu"]` parses to a value
above zero. That key is the standard NVIDIA device-plugin resource, so it works on any
cluster; a nodegroup label would not. An absent or unparseable value counts as zero rather
than failing the read. Only `metadata.name`, `status.capacity["nvidia.com/gpu"]` and
`status.nodeInfo.architecture` are decoded.

Node data has its own cache with the same TTL as the application snapshot, so a node list
that changes slowly is not refetched per request. A failed read is cached too, so an
operator who enabled the flag without the ClusterRole does not hammer the API server.

### Why it is opt-in

The default install holds a namespaced `Role` and deliberately no ClusterRole: the blast
radius is provably one namespace. Nodes are cluster-scoped, so reading them is impossible
without a ClusterRole. Rather than widen the default permissions for a nice-to-have panel,
node stats are opt-in:

- `NODE_STATS` defaults to `false`. The nodes API is then never called and the section is
  not rendered. Default behaviour and default RBAC are unchanged.
- Turning it on requires `rbac.clusterRole=true` in the chart, or uncommenting the
  ClusterRole block in `deploy/install.yaml`. It grants `get`/`list` on `nodes` and nothing
  else.
- If the flag is on and the ClusterRole is missing, the read is denied and the page still
  renders: the capacity section shows a one-line note saying a ClusterRole is needed. An
  optional feature that is denied degrades, it never breaks the page.

## Caching

One snapshot is cached for `CACHE_TTL_SECONDS`. There is no background refresh goroutine — the
first request after expiry does the work. The cache mutex is deliberately held across the upstream
fetch, so a burst of concurrent requests collapses into exactly one call to the Kubernetes API.

If a refresh fails and a previous snapshot exists, that snapshot keeps being served, marked stale,
and the page shows both a stale banner and the error. A transient API outage degrades the page
gracefully instead of blanking it.

## Kubernetes access

The client is built from the standard service account mount:

- **Token**: read from `/var/run/secrets/kubernetes.io/serviceaccount/token` on **every** request.
  Projected tokens rotate, so caching the value at startup breaks the app roughly an hour in.
- **CA**: read once from `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt` into an
  `x509.CertPool` used as `tls.Config.RootCAs`.
- **URL**: `https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT/apis/argoproj.io/v1alpha1/namespaces/<ns>/applications`
- One shared `http.Client` with a 10s request timeout; response bodies are capped at 16 MiB.

The pod needs `get`/`list` on `applications.argoproj.io` in `ARGOCD_NAMESPACE` and nothing else,
unless `NODE_STATS` is enabled, which adds `get`/`list` on `nodes` cluster-wide.
The RBAC and the Deployment live in the chart, not in this repo.

Only the fields actually rendered are decoded (`internal/argocd/types.go`). Some Applications
report a `null` sync revision; that decodes to an empty string and is rendered as no revision.

## Running locally

`scripts/local-test.sh` builds the binary and wires it up for you:

```sh
./scripts/local-test.sh fixture              # offline, serves testdata/applications.json
./scripts/local-test.sh cluster <context>    # live, via kubectl proxy
```

The kube context is a required argument in `cluster` mode — the script has no built-in default for
it, nor for `CLUSTER_NAME`, `ARGOCD_UI_BASE`, `REGION` or `ENV_TYPE`. Export any of those to
override them. Fixture mode sets `ROOT_APP_NAME=root-app` to match the fixture's root application;
cluster mode uses the real default, `ocp-services`.

By hand, against a proxied cluster API:

```sh
kubectl config use-context <your-context>
kubectl proxy --port=8001 &

KUBE_API_URL=http://127.0.0.1:8001 \
ENV_NAME=sample-dev ENV_TYPE=dev REGION=eu-west-1 \
ARGOCD_NAMESPACE=argocd ROOT_APP_NAME=ocp-services \
BASE_PATH=/srv-status PORT=8080 \
go run ./cmd/srv-status
```

Then open <http://localhost:8080/srv-status/>.

`kubectl proxy` authenticates on your behalf, so no token is needed; `KUBE_TOKEN_FILE` defaults to
`/dev/null` and an empty bearer token is sent. `KUBE_API_URL` exists purely for this workflow — in
a cluster it is unset and the in-cluster mount is used.

Tests and checks:

```sh
gofmt -l .
go vet ./...
go build ./...
go test ./... -race -cover
```

`testdata/applications.json` is a synthetic fixture. Every application name in it is invented and
every `repoURL` points at a reserved `.invalid` host. It contains no real cluster data.

## Publishing

This repository is safe to publish:

- No credentials, tokens, private keys, account IDs or customer data are committed. The service
  account token is read from the pod mount at runtime and is never logged.
- `testdata/applications.json` is synthetic — invented application names, `.invalid` repository
  hosts, and fabricated revisions.
- Deployment-specific values (`CLUSTER_NAME`, `ARGOCD_UI_BASE`, `ENV_NAME`, `ENV_TYPE`, `REGION`,
  `IGNORE_GLOBS`) are supplied at runtime through environment variables. Nothing environment- or
  cluster-specific is baked into the source, the image or the local test script.

The module path is `github.com/ntmggr/srv-status`:

```sh
go get github.com/ntmggr/srv-status
```

## Build

```sh
docker build --build-arg VERSION=0.1.0 -t srv-status:0.1.0 .
```

Two stages: a `golang:1.23-bookworm` builder producing a static `CGO_ENABLED=0` binary, and a
`gcr.io/distroless/static-debian12:nonroot` runtime. The image runs as uid 65532 with a read-only
root filesystem and needs no writable volume.

`main.version` is stamped via `-ldflags -X` and shown in the page footer.

## Deployment

Deployed per cluster by ArgoCD from the platform chart repository, as one Application per
environment. The chart owns the Deployment, Service, ServiceAccount, RBAC, Ingress and the
environment-specific values (`ENV_NAME`, `ENV_TYPE`, `REGION`, `CLUSTER_NAME`, `BASE_PATH`,
`ARGOCD_UI_BASE`, `IGNORE_GLOBS`). This repository ships the binary and the image only.

Because the app serves everything under `BASE_PATH`, it can be exposed at a subpath of an existing
host without a rewrite rule on the ingress.

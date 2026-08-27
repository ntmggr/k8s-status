# Local demo

`hack/demo.sh` stands up a throwaway Kubernetes cluster, fills it with made-up ArgoCD
`Application` objects, and runs `k8s-status` against it. It exists so you can see the
tool work on real cluster objects without access to a real cluster.

It is not the same as `./scripts/local-test.sh fixture`. That mode serves a JSON file
over HTTP and never touches Kubernetes. This one goes through the actual API server:
a real CRD, real objects, real `kubectl proxy`, real reads.

## Prerequisites

| Tool | Why |
|---|---|
| [`kind`](https://kind.sigs.k8s.io/) | creates the throwaway cluster |
| `kubectl` | applies the CRD and the sample objects, and proxies the API |
| `docker` (running) | kind runs the cluster as a container |
| `go` | builds the binary |

The script checks all four up front and names whatever is missing. It also uses
`curl` and `python3` to wait for the port and to print the summary at the end; both
are standard on macOS and on GitHub runners, and if either is absent the page still
serves, you just do not get the summary.

## Run it

```sh
hack/demo.sh
```

First run takes a couple of minutes, mostly pulling the kind node image. It prints:

```
  page   http://127.0.0.1:8080/k8s-status/
  json   http://127.0.0.1:8080/k8s-status/api/status
  health http://127.0.0.1:8080/k8s-status/healthz
```

Open the first URL. `Ctrl-C` stops the proxy and the app; the cluster stays up so the
next run starts in seconds.

Ports are configurable if 8080 or 8001 are already taken:

```sh
PORT=8099 PROXY_PORT=8098 hack/demo.sh
```

## What you should see

Nine services, one per interesting state, plus the root app-of-apps that is not shown
as a row:

```
summary: {"total": 9, "ok": 3, "degraded": 1, "warning": 1, "progressing": 1,
          "drift": 1, "prune": 1, "suspended": 1, "hidden": 0, "gpu": 1}

  payments-api     DEGRADED     4.2.0      Synced
  metrics-relay    WARNING      1.4.7      Synced
  checkout-web     PROGRESSING  3.0.0-rc.4 OutOfSync
  search-api       DRIFT        7.1.2      OutOfSync
  legacy-batch     PRUNE        -          OutOfSync
  nightly-report   SUSPENDED    2.0.1      Synced
  admin-ui         OK           1.15.0     Synced
  orders-api       OK           2.4.1      Synced
  transcode-gpu    OK           0.9.3      Synced
```

Three of those rows are worth looking at twice, because they are the states that are
easy to get wrong:

- **`legacy-batch` reads `PRUNE`, not `DEGRADED`**, even though its health is
  `Missing`. Prune is checked before health, so an Application that Git no longer asks
  for is reported as housekeeping rather than as an outage.
- **`search-api` reads `DRIFT`, not `PROGRESSING`.** It is `OutOfSync` but healthy,
  and the root app has finished syncing, so this is settled drift.
- **`checkout-web` reads `PROGRESSING`, not `DRIFT`.** Also `OutOfSync`, but its
  health is `Progressing`, and that rule outranks drift so a normal rollout does not
  set off an alarm.

`transcode-gpu` carries the GPU chip: its name matches the `*-gpu` pattern in the
default `GPU_GLOBS`, which is name-based and needs no extra permissions.

Both optional sections are on, because your kubeconfig against kind is
cluster-admin and can read them:

- **Cluster capacity**, one node, arm64 or amd64 depending on your machine, zero GPUs.
- **Workloads ArgoCD does not manage**, four rows: `coredns`, `kindnet`,
  `kube-proxy` and `local-path-provisioner`. These are what kind installs itself, and
  nothing in the (fake) ArgoCD owns them, which is exactly the point of that section.

Set `NODE_STATS=false` or `UNMANAGED=false` to turn either off.

## What the script actually does

1. Checks `kind`, `kubectl`, `docker` and `go`, and that the Docker daemon responds.
2. Creates the kind cluster `k8s-status-demo`, or reuses it if it is already there.
3. Applies `hack/application-crd.yaml`, then waits for it to be Established.
4. Applies `hack/demo-apps.yaml`, the `argocd` namespace, the `platform` root app
   and nine children.
5. Reads every object back and fails loudly if any of them lost its `status`.
6. Builds the binary, starts `kubectl proxy`, and starts `k8s-status` pointed at the
   proxy through `KUBE_API_URL`.

### Only the CRD, not ArgoCD

`k8s-status` reads `Application` objects and nothing else. Installing all of ArgoCD
would add several minutes and a controller that would immediately try to reconcile
nine applications against repositories that do not exist. The CRD plus static objects
gives the same input with none of that.

`hack/application-crd.yaml` is therefore a minimal stand-in, not a copy of the
upstream CRD. Its schema is open (`x-kubernetes-preserve-unknown-fields`) instead of
reproducing the full ArgoCD spec.

### Why the CRD has no status subresource

This is the one non-obvious part. If a CRD declares `subresources.status`, the API
server strips `status:` from anything written to the main resource, so a plain
`kubectl apply` of the sample objects would store the spec and silently discard the
status. Every row would then render as `WARNING`, because an empty health string
normalises to `Unknown`.

Two ways round it: write each status through the `/status` endpoint with
`kubectl replace --raw`, or leave the subresource off. The demo leaves it off, which
means one `kubectl apply -f` does the whole job. Step 5 above verifies it worked
rather than trusting it.

Real ArgoCD does declare the status subresource. This CRD is for the demo only, do
not apply it to a cluster that runs ArgoCD.

## Re-running

Safe to run as many times as you like. Cluster creation is skipped when the cluster
exists, and every `kubectl apply` is a no-op when nothing changed. Edit
`hack/demo-apps.yaml` and re-run to see a different mix of states.

## Cleaning up

```sh
hack/demo.sh --cleanup
```

Deletes the `k8s-status-demo` kind cluster and nothing else. Any other kind cluster on
your machine is left alone. If the cluster is already gone the command says so and
exits cleanly.

The sample data is entirely invented: every application name is made up and every
`repoURL` points at the reserved `.invalid` TLD, so nothing in this directory can
resolve to a real host.

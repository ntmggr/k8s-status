# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.0] - 2026-08-24

### Added

- Optional cluster capacity section behind `NODE_STATS` (default `false`): total nodes, CPU nodes,
  GPU nodes, total GPUs, GPU services and the architecture split, read from `/api/v1/nodes`. GPU
  nodes are detected via `nvidia.com/gpu` in `.status.capacity`, which is portable across clusters.
  Surfaced in `/api/status` under `nodes`, omitted when the feature is off.
- `internal/kube`: a small standard-library client for the local Kubernetes API server, holding the
  service account token, cluster CA and request plumbing that `internal/argocd` now builds on.
- Chart: `config.nodeStats` and `rbac.clusterRole`, both `false` by default. When enabled the chart
  renders a ClusterRole and ClusterRoleBinding granting exactly `get`/`list` on `nodes`; the default
  render still contains no cluster-scoped object. `deploy/install.yaml` ships the same block
  commented out.
- Server-side filtering of the service table by `?status=`, `?sync=` and `?gpu=`, repeatable and
  comma-separated, case-insensitive, ANDed across parameters and ORed within one. Status tiles are
  toggle links, active filters render as removable chips with a `showing N of M` count, and an
  unmatched value renders an explicit empty state instead of the full list. `/api/status` applies
  the same filters and echoes them in a `filters` object.
- `?refresh=<seconds>` overrides `REFRESH_SECONDS` per view, with `0` disabling auto-refresh
  entirely. Values are clamped to 5-3600 seconds; invalid input falls back to the configured
  default. Filter and refresh links are built from the current query, so the two compose.

### Changed

- GPU is no longer a status tile. The status tiles are mutually exclusive and sum to the total,
  while a GPU service is already counted in `OK` or `DEGRADED`; the count moved to the capacity
  section. The per-row `GPU` chip and the row edge accent are unchanged.
- The single `Version` column is split into `App version` and `Chart version`; the chart column
  carries `hide-sm` and drops on narrow screens.

### Fixed

- The sticky table header now actually sticks: `overflow: hidden` on `.card` was making the card the
  sticky element's scroll box. Rounded corners now come from the table's own first/last cells. The
  page header line is sticky too, and both offsets share one `--hdr-h` custom property.

## [0.3.0] - 2026-08-24

### Added

- `charts/srv-status`: a lean, hand-written Helm chart for installing the service as a long-running
  extra service on any EKS cluster. Templates are limited to ServiceAccount, Role/RoleBinding,
  ConfigMap, Deployment, Service and optional Ingress/Istio VirtualService, both disabled by
  default. `checksum/config` pod annotation rolls the Deployment on config change.
- Chart README documenting every value, the namespaced-Role rationale, why no IRSA annotation is
  set, and both the `helm install` and ArgoCD `Application` install paths.

### Changed

- `scripts/install.sh` gained a `--helm` mode that installs via the chart; the default raw-manifest
  path (`deploy/install.yaml`) is unchanged and remains the no-Helm escape hatch.
- `scripts/install.sh` usage examples no longer reference an internal ECR registry.

## [0.2.0] - 2026-08-24

### Changed

- Module path renamed from the internal GitLab host to `github.com/ntmggr/srv-status`; all imports
  updated.
- `testdata/applications.json` application names replaced with generic, obviously synthetic names
  (`orders-api`, `search-api`, `media-encoder`, ...) and the fixture root app renamed to `root-app`.
  Tests updated accordingly. The runtime `ROOT_APP_NAME` default is unchanged (`ocp-services`).
- `scripts/local-test.sh` no longer carries environment-specific defaults: `ARGOCD_UI_BASE`,
  `CLUSTER_NAME`, `REGION` and `ENV_TYPE` default to empty, `ENV_NAME` defaults to `local`, and the
  kube context is now a required argument in `cluster` mode.
- README documents the local test script and adds a Publishing section.

## [0.1.0] - 2026-08-24

### Added

- Initial release of `srv-status`, a read-only per-cluster status page for the OCP platform.
- In-cluster ArgoCD reader built on the standard library only: service account token re-read per
  request, cluster CA pinned via `x509.CertPool`, 10s request timeout, 16 MiB response cap.
- State model deriving `OK` / `DEGRADED` / `SYNCING` / `PRUNE` / `SUSPENDED` per service from the
  child Application, with prune orphans taken from the root app's `requiresPruning` resources.
- Severity-then-name sorting, `IGNORE_GLOBS` hiding with a separate `hidden` count, and summary
  counters.
- Mutex-guarded snapshot cache with `CACHE_TTL_SECONDS` TTL, single-flight refresh and
  stale-on-error serving.
- HTTP routes under `BASE_PATH`: HTML page, `/api/status` (always HTTP 200), `/healthz` that does
  not depend on the Kubernetes API, and a redirect from the un-slashed base path.
- Server-rendered HTML page with inline CSS, meta-refresh, no JavaScript and no external assets.
- Two-stage distroless Dockerfile running as uid 65532 with a read-only root filesystem.

# Security Policy

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub, using
[Report a vulnerability](https://github.com/ntmggr/k8s-status/security/advisories/new)
on the Security tab. That opens a private advisory visible only to the maintainers.

Include what you found, how to reproduce it, and what an attacker could do with it.
You should get a first response within a week. If the report is valid, a fix and an
advisory will follow, and you will be credited unless you prefer otherwise.

## Supported versions

Only the latest released version is supported. Fixes go into a new release rather
than being backported.

## What this project does, and what that means for risk

`k8s-status` is read only. It asks the Kubernetes API for ArgoCD Applications, and
optionally for Flux resources, nodes, and workloads. It never writes to a cluster,
and its ServiceAccount cannot: the default is a namespaced Role granting `get` and
`list` on `argoproj.io/applications` in one namespace, with no ClusterRole at all.
The optional node and unmanaged workload features need a ClusterRole, which is why
they are off by default.

The page itself has **no authentication**. That is deliberate: it is meant to sit
behind a VPN, an internal load balancer, or a private subnet. If you expose it more
widely, put an authenticating proxy in front of it. Anyone who can reach the page
can see your service names, versions, health, and, if enabled, node counts.

The container runs as uid 65532, non root, with a read only root filesystem and all
capabilities dropped. The image is built from `distroless/static`, so it contains no
shell and no package manager. The binary has no third party Go dependencies, which
keeps the supply chain to the Go standard library alone.

Every build runs Trivy for vulnerabilities and secrets, and Dockle for CIS Docker
Benchmark checks. A fixable HIGH or CRITICAL finding fails the pipeline.

## Scope

In scope: anything that lets the service write to a cluster, read beyond its granted
RBAC, leak data to an unauthorised party, or execute code.

Out of scope: the lack of authentication on the page, which is documented above and
by design, and findings that require an attacker to already have cluster admin.

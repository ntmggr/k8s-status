# Build stage. Pinned to a specific Go minor so a toolchain bump is a deliberate commit.
# Keep this current: the stdlib CVEs Trivy reports are fixed by the toolchain, not by us.
FROM golang:1.27-bookworm AS build

ARG VERSION=dev
WORKDIR /src

# Copy only what the build needs; testdata, docs and charts never enter the image.
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

# CGO off gives a fully static binary, which is what lets the runtime image be
# distroless-static: no libc, no shell, no package manager.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/k8s-status ./cmd/k8s-status

# Runtime stage.
#
# distroless/static contains no shell, no package manager, no busybox and no libc
# — only CA certificates, timezone data and /etc/passwd. That removes most of what
# the CIS Docker Benchmark asks you to remove or justify, rather than hardening it
# after the fact. The :nonroot variant ships a 65532 user so the image cannot be
# run as root by accident.
#
# See README "Container hardening" for how this maps to the CIS controls.
FROM gcr.io/distroless/static-debian12:nonroot

# OCI metadata, so a scanner or registry can attribute the image (CIS 4.11).
LABEL org.opencontainers.image.title="k8s-status" \
      org.opencontainers.image.description="Read-only per-cluster status page for ArgoCD-managed services" \
      org.opencontainers.image.source="https://github.com/ntmggr/k8s-status" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/k8s-status /k8s-status

# Numeric UID/GID, not a name: kubelet can enforce runAsNonRoot without resolving
# /etc/passwd, and it satisfies CIS 4.1 "create a user for the container".
USER 65532:65532

EXPOSE 8080
ENTRYPOINT ["/k8s-status"]

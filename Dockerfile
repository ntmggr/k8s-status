FROM golang:1.23-bookworm AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/srv-status ./cmd/srv-status

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/srv-status /srv-status

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/srv-status"]

# syntax=docker/dockerfile:1
# Production image: serk-api, serk-migrate, serk-relay and serk-signer only.
# Development tools (seed data, dev tokens) live in deploy/Dockerfile.devtools
# and are never built from this file (internal/config tests enforce it).
ARG GO_IMAGE=golang:1.26.10-alpine
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/serk-api ./cmd/serk-api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/serk-migrate ./cmd/serk-migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/serk-relay ./cmd/serk-relay \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/serk-signer ./cmd/serk-signer

# /run/serk: the signer's socket directory, owned by the unprivileged user so a
# named volume mounted there inherits that ownership (distroless has no shell).
FROM build AS rundir
RUN mkdir -p /rootfs/run/serk && chown 65532:65532 /rootfs/run/serk && chmod 700 /rootfs/run/serk

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/serk-api /out/serk-migrate /out/serk-relay /out/serk-signer /
COPY --from=rundir --chown=65532:65532 /rootfs/run/serk /run/serk
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/serk-api"]

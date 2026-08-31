FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
ENV GOPROXY=https://proxy.golang.org,direct
# Dependency downloads occasionally fail with a transient EOF. Keep this in a
# separate cached layer and retry so a flaky module-proxy connection does not
# discard the whole compile step.
RUN downloaded=0; \
    for attempt in 1 2 3 4 5; do \
      if go mod download; then downloaded=1; break; fi; \
      echo "go module download failed (attempt ${attempt}/5), retrying" >&2; \
      sleep $((attempt * 2)); \
    done; \
    test "${downloaded}" = 1
COPY cmd ./cmd
COPY internal ./internal
COPY authz ./authz
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-worker-rollout ./cmd/worker-rollout && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-migrate ./cmd/migrate

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app && mkdir -p /var/lib/project-management && chown app:app /var/lib/project-management
COPY --from=builder /out/project-api /usr/local/bin/project-api
COPY --from=builder /out/project-worker /usr/local/bin/project-worker
COPY --from=builder /out/project-worker-rollout /usr/local/bin/project-worker-rollout
COPY --from=builder /out/project-migrate /usr/local/bin/project-migrate
USER app
ENV PM_HTTP_ADDR=:8082
EXPOSE 8082 9092
ENTRYPOINT ["/usr/local/bin/project-api"]

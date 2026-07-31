FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY authz ./authz
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-migrate ./cmd/migrate

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app && mkdir -p /var/lib/project-management && chown app:app /var/lib/project-management
COPY --from=builder /out/project-api /usr/local/bin/project-api
COPY --from=builder /out/project-worker /usr/local/bin/project-worker
COPY --from=builder /out/project-migrate /usr/local/bin/project-migrate
USER app
ENV PM_HTTP_ADDR=:8082
EXPOSE 8082
ENTRYPOINT ["/usr/local/bin/project-api"]

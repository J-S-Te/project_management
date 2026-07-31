FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-api ./cmd/api

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app && mkdir -p /var/lib/project-management && chown app:app /var/lib/project-management
COPY --from=builder /out/project-api /usr/local/bin/project-api
USER app
ENV PM_HTTP_ADDR=:8082 PM_DATA_FILE=/var/lib/project-management/data.json
EXPOSE 8082
ENTRYPOINT ["/usr/local/bin/project-api"]

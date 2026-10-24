FROM golang:1.25.4-alpine@sha256:d3f0cf7723f3429e3f9ed846243970b20a2de7bae6a5b66fc5914e228d831bbb AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/license-core ./third_party/license-core
COPY scripts/license-core-sync.sh scripts/license-core.sha256 ./scripts/
RUN sh scripts/license-core-sync.sh --check
ARG GOPROXY=https://goproxy.cn|https://proxy.golang.org|direct
ARG GOSUMDB=sum.golang.google.cn
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}
# 依赖下载偶尔因暂时性的 EOF 失败。单独使用可缓存的构建层并重试，
# 避免模块代理连接不稳定时
# 丢弃整个编译步骤。
RUN downloaded=0; \
    for attempt in 1 2 3 4 5; do \
      if go mod download && go mod verify; then downloaded=1; break; fi; \
      echo "go module download failed (attempt ${attempt}/5)" >&2; \
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
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-background-control ./cmd/background-control && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-migrate ./cmd/migrate && \
    # SLA 主动提醒的周期扫描任务：与 API 分离，避免把周期任务塞进请求路径。
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sla-notifier ./cmd/sla-notifier

FROM alpine:3.22@sha256:d406446bc05b82480756ce8f1287964d28768c23955fa9480d2a74a2eae7a654
ARG APP_VERSION
LABEL org.opencontainers.image.version=${APP_VERSION} com.basic-platform.license.protocol="1" io.uip.project-background.protocol="1"
# tzdata 使 TZ=Asia/Shanghai 与 DSN loc=Asia/Shanghai（go-sql-driver 解析时需要）在运行镜像内可用。
RUN apk add --no-cache ca-certificates tzdata && addgroup -S app && adduser -S -G app app && mkdir -p /var/lib/project-management /var/lib/commercial-license && chown app:app /var/lib/project-management /var/lib/commercial-license
COPY --from=builder /out/project-api /usr/local/bin/project-api
COPY --from=builder /out/project-worker /usr/local/bin/project-worker
COPY --from=builder /out/project-worker-rollout /usr/local/bin/project-worker-rollout
COPY --from=builder /out/project-background-control /usr/local/bin/project-background-control
COPY --from=builder /out/project-migrate /usr/local/bin/project-migrate
COPY --from=builder /out/sla-notifier /usr/local/bin/sla-notifier
USER app
ENV PM_HTTP_ADDR=:8082
EXPOSE 8082 9092
ENTRYPOINT ["/usr/local/bin/project-api"]

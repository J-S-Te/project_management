# 项目服务内容管理系统

首版可运行实现包含：

- Gin HTTP API：项目列表与创建、服务项查询与拆解确认、配置规则查询/创建/启停、仪表盘汇总。
- GORM + MySQL 持久化，所有业务查询强制携带 OIDC 会话中的 `tenant_id`。
- Temporal 承载服务项拆解确认工作流；Activity 可重试且以目标状态写入保证幂等。API 默认内嵌 Worker，也支持独立 Worker 进程。
- 版本化 SQL 迁移由独立 `project-migrate` 入口执行，API 只在迁移成功后启动。
- 与 ER 图一致的项目、服务项和规则核心实体，以及可直接体验完整页面的初始化数据。
- 使用平台注册的 OAuth Client 建立授权码 + PKCE OIDC 会话，不复用平台 Cookie；权限变更通过刷新令牌周期性同步。
- 后端精确校验 `project.read`、`project.create`、`service_item.confirm`、`project_rule.manage`，默认拒绝且不接受通配权限。
- 写操作使用独立机器 OAuth Client 上报平台审计；启动时可将内嵌角色/权限目录同步到平台。
- Vue 前端通过同源 `/project_management/api/v1` 接入，Vite 开发代理默认转发至 `127.0.0.1:8082`。

## 本地运行

本地开发使用项目自己的 Compose 和 `.env.local`。生产发布与其他子系统一致：本仓库独立构建不可变镜像，服务器端由 `platform/deploy/production` 的统一 Compose 和 `bin/deploy-service.sh` 管理数据库备份、迁移、服务更新、健康检查及失败回滚。首次接入时需要通过平台管理 API/控制台登记 `project_management` Application、Environment、Login Target 和独立 OAuth Client，并把受控凭据写入服务器 `/opt/basic-platform/.env`。

生产接入前必须先发布不可变镜像：本仓库 `main` 分支的 CI/CD 会把镜像推送到 ACR，并调用服务器端 `deploy-service.sh project <镜像@sha256:digest>` 把 `PROJECT_IMAGE` 写入 `/opt/basic-platform/.release.env`。若服务器上 `.release.env` 缺少 `PROJECT_IMAGE` 或仍是可变 tag，平台接入预检会以“镜像必须是不可变 digest”拒绝；重新运行本仓库 CI（或再次推送 main）即可完成镜像发布。

直接在宿主机运行后端时加载项目自己的环境文件：

```bash
set -a; source .env.local; set +a
make run
```

本地 Docker：

```bash
docker compose --env-file .env.local up -d --build
```

生产镜像由本仓库 `.github/workflows/ci-cd.yml` 构建并按 digest 发布，远端统一调用 `/opt/basic-platform/bin/deploy-service.sh project`。

服务默认监听 `:8082`，依赖 MySQL 与 Temporal。另一个终端在 `frontend/` 运行：

```bash
npm run dev
```

访问 `http://127.0.0.1:5173/project_management/dashboard`。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `PM_HTTP_ADDR` | `:8082` | HTTP 监听地址 |
| `MYSQL_DSN` | 无 | 项目管理 MySQL DSN，必须启用 `parseTime=true` |
| `TEMPORAL_ADDRESS` / `TEMPORAL_TASK_QUEUE` | `localhost:7233` / `project-management` | Temporal 服务与任务队列 |
| `PROJECT_RUN_WORKER_WITH_API` | `true` | API 是否内嵌 Temporal Worker；拆分部署时关闭并启动 `cmd/worker` |
| `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | 无 | 平台签发的项目系统浏览器客户端 |
| `OIDC_REDIRECT_URI` / `OIDC_TENANT_ID` | 无 | 回调地址和固定租户边界 |
| `PROJECT_PLATFORM_BACKCHANNEL_BASE_URL` | `http://platform-api:8080` | Compose 容器访问平台 API 与 OIDC 后通道的内部地址；与浏览器使用的公开 `OIDC_ISSUER` 分离 |
| `PLATFORM_AUDIT_CLIENT_*` | 空 | 具备 `audit.ingest` 的机器客户端；配置后上报写操作审计 |
| `PLATFORM_AUTHORIZATION_CATALOG_*` | 空 | 具备 `authorization.catalog.sync` 的机器客户端 |

OIDC Client Secret、数据库口令和机器客户端 Secret 只能通过运行时 Secret 注入，不得提交到仓库或写入前端环境变量。前端按钮权限只影响展示，服务端权限校验才是安全边界。MySQL 与 Temporal 均应使用独立持久卷或托管服务。

## API

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/healthz` | 存活检查 |
| GET | `/auth/login`、`/auth/callback`、`/auth/logout` | 项目系统 OIDC 会话 |
| GET | `/api/v1/auth/me` | 当前项目系统主体与权限 |
| GET | `/api/v1/dashboard` | 汇总指标 |
| GET/POST | `/api/v1/projects` | 查询/创建项目 |
| GET | `/api/v1/projects/{id}` | 项目详情 |
| GET | `/api/v1/service-items` | 查询服务项 |
| POST | `/api/v1/service-items/confirm` | 确认拆解结果 |
| GET/POST | `/api/v1/rules` | 查询/创建规则 |
| PATCH | `/api/v1/rules/{id}` | 启停规则 |

## 验证

```bash
make test
```

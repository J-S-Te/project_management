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
| `CONTRACT_INTEGRATION_ENABLED` | `false` | 是否启用合同系统内部接收接口 |

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
| POST | `/api/v1/contracts/activate` | 接收生效合同，幂等生成项目与独立服务项 |
| POST | `/internal/v1/contracts/activate` | 合同系统内部网络投递入口，不使用浏览器会话 |
| POST | `/api/v1/projects/{id}/decomposition-adjustments` | 调整拆解并记录补充协议引用 |
| POST | `/api/v1/service-items/{id}/team-assignment` | 业务管理员分配团队负责人 |
| POST | `/api/v1/service-items/{id}/execution-assignment` | 团队负责人指派项目经理、工程师和设备并校验能力 |
| POST | `/api/v1/service-items/{id}/implementation-plan` | 项目经理发布现场计划；渗透测试项必须包含专项计划 |
| POST | `/api/v1/service-items/{id}/preparation` | 登记设备申领和行程预定 |
| POST | `/api/v1/service-items/{id}/check-in` | 记录带时间戳的 GPS 签到 |
| POST | `/api/v1/service-items/{id}/field-records` | 提交原始数据、环境条件和证据文件引用 |
| POST | `/api/v1/service-items/{id}/deviations` | 停止任务并上报偏离 |
| POST | `/api/v1/deviations/{id}/review` | 团队负责人或技术总监决定放行、终止或重测 |
| POST | `/api/v1/projects/{id}/field-complete` | 项目经理汇总确认现场实施完成 |
| GET/PUT | `/api/v1/capabilities` | 查询或维护人员资质、设备能力与有效期 |
| GET | `/api/v1/delivery-events` | 查询完整交付过程留痕 |

### 外部系统边界

本模块保存业务状态、外部单据编号和文件 URL，不伪造其他系统的处理结果。完整自动闭环仍需要：

- 合同生效事件已通过事务型 Outbox、内部网络接口和 `contract_id + contract_version` 幂等键自动生成项目；内部接口不得通过公网或门户网关暴露。
- 拆解调整当前记录外部 `supplement_contract_id`；自动创建补充协议仍需合同系统明确主合同关联、金额/服务变更规则和审批发起人语义。
- 组织/人事系统提供团队、负责人、项目经理和工程师的稳定用户 ID，以及资质证书的签发、吊销和有效期数据。
- 设备系统提供设备 ID、能力码、校准有效期、占用排期和设备申领单状态；差旅系统提供行程预定单状态。
- 文件服务提供受控上传、病毒扫描、内容哈希、EXIF/拍摄时间校验和长期留存；现场接口当前只保存证据 URL。
- 移动端提供可信定位权限、定位精度、设备标识、离线补传和防篡改签名；当前后端校验经纬度与签到时间窗口。
- 通知/待办系统消费偏离、冲突、资质到期和评审事件，向团队负责人及技术总监派发待办。

## 验证

```bash
make test
```

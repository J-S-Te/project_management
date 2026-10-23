# 项目服务内容管理系统

核验基准：2026-10-23，当前工作区源码。本文是工程入口，不代表目标环境已部署。历史静态记录见[2026-10-22 文档同步报告](../.ai/qa/docs-code-sync-20261022/agents/project-bi-settlement-report.json)；本轮实现与隔离验证见[任务验收记录](../.ai/qa/project-temporal-bi-20261023/verification.md)。

- [项目管理操作手册](../sharedDocs/04_项目管理/README.md)：角色入口、项目建立、拆解、分配、实施、报告、导入和配置。
- [当前业务模型及历史记录](docs/business-model.md)：状态机和约束；历史裁定、事故与测试结果有单独标识。
- [2026-10-13 历史源码核验与文档纠正记录](../.ai/modules/operation-docs-project-20261013.md)：逐条证据链、限制和检查范围。

## 当前实现与运行边界

Gin API、GORM/MySQL 与 Vue 工作区共同完成项目交付。浏览器经同源 `/project_management/api/v1` 请求；OIDC 授权码 + PKCE 建立本地持久化会话，服务端在线取得平台授权上下文，并结合权限码、租户、数据范围与执行责任链限制读取和写入。前端按钮可见性不是安全边界。

合同审批后的事务 Outbox 可通过受机器身份保护的内部激活接口建立项目；手工补建必须从合同系统读取已审批合同及权威服务清单。项目状态和进度由全部有效服务项派生，不能由浏览器任意设置。建项以租户 + 稳定合同 ID 去重（历史空 ID 回退合同号），普通合同版本变化不再允许重复建项；自动激活返回既有项目，人工重复返回 409。人工与自动建项、拆解调整均从启用检测类别取得必检能力码基线并写入服务项，客户端只能追加要求。

拆解确认直接执行带范围过滤和行锁的 MySQL 事务。API 不启动后台 Worker，`PROJECT_RUN_WORKER_WITH_API` 已弃用。`cmd/sla-notifier` 与兼容入口 `cmd/worker` 委托同一后台 Runtime；生产仍只部署 project-api 和 project-sla-notifier 两个组件。新模板选择 Temporal，按可信租户和环境隔离 SLA 定时、派生事件及通知重试；人工审批与项目状态仍由 MySQL 事务推进。旧配置未指定 engine 时使用 legacy；依赖失败不会自动切换 engine。

迁移 `000033_contract_project_identity.sql` 会先检查稳定身份及合同号的存量重复；有冲突即中止，不删除或合并旧项目。发布前应按迁移文件头执行获授权的只读预检，业务确认处置后再走受控迁移。本轮未检查业务库。

现场支持浏览器离线队列、定位签到、文件网关证据上传、签字图片和客户端操作幂等。报告支持逐级编制/审核/签发/归档、身份分离及已签发报告更正版本。设备档案、占用和差旅快照属于本模块本地业务记录，不表示外部设备系统或差旅审批已办结；定位不含地理围栏或可信硬件防伪。

通知有持久化 Outbox 和独立 `cmd/sla-notifier` 消费者。统一生产和本模块本地 Compose 均装配该消费者；旧独立 compose.production.yaml 需逐项对齐新配置，不能代替统一受控发布。接口成功不等于提醒已经送达。

## 开发与发布入口（本次未执行）

从本模块目录运行，先按实际环境准备配置，不能使用生产 Secret 做演示。数据库迁移必须由受控迁移入口执行；`make run` 本身不执行迁移。

```bash
make migrate
make run
```

环境文件只能在可信本机按已有流程加载；不要把数据库口令、OIDC Secret 或机器客户端 Secret 写入本文或前端变量。前端在仓库 `frontend/` 按其依赖安装流程运行 `npm run dev`，本地入口路径为 `/project_management/dashboard`；Vite 默认代理后端 `127.0.0.1:8082`。

本地容器入口是 [compose.yaml](compose.yaml)，独立生产入口是 [compose.production.yaml](compose.production.yaml)。统一生产入口见 [平台生产 Compose](../platform/deploy/production/compose.yaml) 和 [受控发布脚本](../platform/deploy/production/bin/deploy-service.sh)。这些是源码中的部署入口，本文不授权执行发布，不保证推送 main 就能修复线上问题。

API 启动会检查迁移记录；待迁移时仍可能监听端口，`/readyz` 返回 `migrations_pending`，不能只用 `/healthz` 判定业务就绪。统一 Compose 的 API 存活探针当前使用 `/healthz`，仍须核对就绪检查和迁移结果。

## 关键配置与装配

以 [配置解析](internal/config/config.go)、[API 装配](cmd/api/main.go)、[SLA 消费者](cmd/sla-notifier/main.go) 为准。下表中的“启用”仍需目标环境凭据、路由、平台权限及依赖可达。

| 配置 | 当前含义 / 未配置影响 |
| --- | --- |
| `PM_HTTP_ADDR`、`MYSQL_DSN` | 默认监听 `:8082`；DSN 必填，应满足现有驱动时间解析约束。 |
| `PLATFORM_BASE_URL`、应用/环境编码 | 平台授权、人员目录、审计等的基础边界；平台地址必须显式配置。Compose 内网覆盖与公开浏览器地址分开管理。 |
| `OIDC_ISSUER`、Client、回调、租户、`OIDC_SESSION_ENCRYPTION_KEY_BASE64` | 必须配置独立浏览器 Client 和 32 字节 Base64 会话加密密钥；Secret 仅运行时注入。Cookie 默认 Secure，本地 HTTP 调试应按既有环境策略配置，不要在生产关闭安全属性。 |
| `OIDC_AUTHORIZATION_REFRESH_INTERVAL` / `OIDC_AUTHORIZATION_MAX_STALE` | 默认 1m / 5m；写请求不能依靠只读陈旧授权继续执行。 |
| `PLATFORM_AUDIT_CLIENT_*`、授权目录同步配置 | 写审计使用机器客户端；授权目录同步须独立配置。当前内置授权目录版本为 8，平台生效角色/数据范围仍需核对。 |
| `OWNER_DIRECTORY_ENABLED` 及目录机器客户端 | 启用并可达后才有平台人员/角色校验；缺失会阻断资质身份复核和相关选择、指派。 |
| `CONTRACT_APPROVAL_VALIDATION_ENABLED` 及合同查询客户端 | 手工创建/补充协议校验的权威来源；缺失不是允许浏览器自报合同数据的理由。 |
| `CONTRACT_INTEGRATION_ENABLED` 及机器身份验证配置 | 默认关闭；启用才注册内部合同激活入口，必须验证 Bearer 机器身份和租户，不接受任意租户头。合同侧还需投递消费者运行。 |
| `FILE_GATEWAY_ENABLED` 及网关客户端 | 默认关闭；证据、报告及三类 CSV 上传依赖实际网关装配，不能以裸 URL 替代。 |
| `NOTIFICATION_ENABLED` 及通知机器客户端 | 开关参与配置校验；实际通知客户端还取决于凭据是否装配，开关为 true 不能单独证明投递可用。 |
| `SLA_SCAN_TENANTS` / `SLA_SCAN_INTERVAL` | 只允许当前可信 OIDC 租户；Temporal 模式缺省补入该租户，默认周期15m。消费者也承担 Outbox 重试，须独立确认存活与送达。 |
| `TEMPORAL_*` | 后台 Temporal 地址、namespace、TLS 和指标连接；拆解确认仍走 MySQL。 |
| `PROJECT_BACKGROUND_ENGINE/BUILD_ID` | 新模板 temporal；Build 由受控发布按完整镜像摘要派生，禁止占位。旧配置缺省 legacy。 |
| `PROJECT_SNAPSHOT_*` | 快照项目/服务项/字节/行大小/时间上限，只能收紧默认保护值。 |

## API 查阅与失败处理

[路由注册与 HTTP 处理](internal/httpapi/api.go) 是接口清单的事实来源；浏览器路径需加公开前缀，内部路径不由浏览器会话调用。

| 入口 | 操作边界 |
| --- | --- |
| `/auth/*`、`/api/v1/auth/me`、`/api/v1/navigation` | 登录、授权主体及角色导航。 |
| `/api/v1/projects`、`/service-items`、`/projects-monitoring` | 项目/服务项和分页实时监控；普通工作区与监控页加载口径不同。 |
| `/internal/v1/contracts/activate` | 受机器令牌保护的合同投递入口；保持内部网络边界。 |
| `GET /api/v1/projects/:id/supplement-contracts` | 项目级补充协议独立目录，需 `project.decomposition.manage` 和目标项目数据范围，无需 `project.create`；`limit` 默认 200、范围 1–200。先取上游最多 200 条摘要再按项目资格过滤，不是全量按客户检索。 |
| `/service-items/confirm`、项目 `decomposition-adjustments` | 确认是批量事务；调整是早期服务项整单替换。目录与提交复用同一资格校验：补充合同须 `ApprovalPassed=true` 且状态为 `approved/active`、同客户，并以权威 ID/编号排除原合同；已关联其他项目不排除补充协议资格。 |
| 服务项 assignment / plan / preparation / field / rollback / report 子路由 | 分阶段权限、状态前置条件及版本校验；不能跨阶段强推。 |
| `/capabilities`、`/equipment` 及 import 子路由 | 人员、设备独立写入口；CSV 返回逐行结果，HTTP 200 仍可能部分失败。 |
| `/rules`、拆解策略/检测类别/覆盖规则子路由 | 应用级配置权限；已有服务项不会因主数据更新自动回填。 |
| `/delivery-events` | 最近事件查询，仓储最多 500 条；不是无限完整历史导出。 |

401 先检查项目会话与平台账号是否一致；403 核对权限和数据范围，不绕过鉴权。409 刷新当前版本并重新核对，不盲目重试批量写入。依赖不可用时由运维核对相应客户端和服务；保存失败先查当前台账/事件，防止已成功的行重复创建。具体业务失败处理见操作手册。

## 验证命令（仅列入口）

```bash
make test
make build
```

测试源码可作为静态规则佐证，集成测试仍需要相应数据库/依赖。本次仅更新文档并检查链接与差异，未运行上述命令、未进行新动态验收、未部署或提交。

## 后台 Runtime 与项目快照（2026-10-23）

Temporal 的专用 queue/deployment 由配置基名、可信环境和租户的完整 SHA256 紧凑编码派生；Build 保留完整镜像摘要，旧 project-management 队列不复用。默认 SLA 15m、派生事件/通知15s，每轮最多50项、运行预算4m、工作流6m。Activity 不自动重试，业务重试、通知12次死信及幂等仍由 MySQL Outbox 管理。

首次 Schedule 暂停，bootstrap 健康用于许可登记；业务就绪另需当前许可、显式版本提升和两类实际 Poller。受控升级先保存私有状态并 pause/drain，再迁移与替换，bootstrap 后 ensure/resume/verify。失败保留候选和现场；人工暂停保留。legacy 暂停只存在于进程内，有原暂停时拒绝自动替换。

合法调度参数变更由同一受控操作迁移。旧进程导出七项非秘密参数（两个间隔、批量数、运行/活动/工作流预算），与旧 Build 一起保存在私有操作记录；回滚在重建旧容器前只恢复这七项，再由显式 restore 核对旧 Build 和规格。凭据及其他配置保留。缺少可信旧参数、结构漂移或排空失败时保留现场并拒绝猜测恢复；镜像和部署资产须配套更新。

在已有统一部署目录内，只读诊断命令为：

```bash
docker compose --env-file .env --env-file .release.env -f docker-compose.yml exec -T project-sla-notifier /usr/local/bin/project-background-control --action inspect
```

返回分别说明 bootstrap_ready、routing_ready、execution_ready 和 paused_reason。inspect 不能代替升级验收；其他控制动作由受控工具携带操作ID调用，不手工修改 operation.json。

精确机器接口 `GET /internal/v1/dashboard/projects/snapshot` 使用 dashboard.project.read Bearer、可信租户及商业 READ_HISTORY。单个只读 RR 事务输出 header/project/end NDJSON，状态、进度、风险、在途和服务项数来自现有领域算法；commit 成功后才发送 end。默认200/批、50,000项目、250,000服务项、64MiB、单行64KiB、30s。取消、超限或失败没有完整成功结束。

本轮源码与本地/隔离测试见[任务验收记录](../.ai/qa/project-temporal-bi-20261023/verification.md)。上述较早“仅文档”记录保留历史含义；真实签名许可、自然ACK、正式服务器及浏览器仍未验收。

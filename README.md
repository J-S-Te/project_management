# 项目服务内容管理系统

首版可运行实现包含：

- Go HTTP API：项目列表与创建、服务项查询与拆解确认、配置规则查询/创建/启停、仪表盘汇总。
- 并发安全的 JSON 文件仓储，采用临时文件 + 原子替换持久化；未配置数据文件时使用内存仓储，便于测试。
- 与 ER 图一致的项目、服务项和规则核心实体，以及可直接体验完整页面的初始化数据。
- 所有写操作输出结构化审计日志；可强制校验可信网关注入的 `X-Authenticated-User` 身份头。
- Vue 前端通过同源 `/project_management/api/v1` 接入，Vite 开发代理默认转发至 `127.0.0.1:8082`。

## 本地运行

```bash
make run
```

服务默认监听 `:8082`，数据写入 `./data/project-management.json`。另一个终端在 `frontend/` 运行：

```bash
npm run dev
```

访问 `http://127.0.0.1:5173/project_management/dashboard`。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `PM_HTTP_ADDR` | `:8082` | HTTP 监听地址 |
| `PM_DATA_FILE` | 空 | JSON 数据文件；空值表示仅内存 |
| `PM_REQUIRE_IDENTITY_HEADERS` | `false` | 生产环境建议设为 `true`，仅接受可信网关注入身份的请求 |

启用身份头校验时，必须由认证网关覆盖（而非透传客户端提供的）`X-Authenticated-User`。前端按钮权限只影响展示，不能替代此服务端边界。生产部署应将结构化日志接入统一审计采集，并把数据文件挂载到持久卷；更高并发部署可在不改变 API 的前提下替换仓储为 MySQL。

## API

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/healthz` | 存活检查 |
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

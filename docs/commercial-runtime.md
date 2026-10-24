# Commercial runtime enforcement

## 当前登记协议与兼容边界（2026-10-22）

受控交付使用目录 V1 / 组件登记协议 V2：分别下发
`COMMERCIAL_LICENSE_REGISTRATION_PROTOCOL=2` 和
`COMMERCIAL_LICENSE_CATALOG_VERSION=1`，组件身份由编译目录限定。登记协议 V2
与签名快照协议 1、镜像标签 `com.basic-platform.license.protocol=1` 是不同版本概念，
不能据镜像标签判断登记仍是 V1。平台仍通过独立发布完整性/批准证据核验实际镜像；
组件更新不以复制旧 coverage/image 绑定或普通业务配置绕过受控发布。

共享 consumer 仍能读取未指定登记版本的 V1 绑定，用于已有组件兼容；受控新交付
不能据此回退。`COMMERCIAL_LICENSE_ENABLED=false` 只允许从未登记且没有其他绑定配置、
固定持久状态目录不存在或为空的组件；已有状态或绑定残留时会拒绝启动，不会因环境
文件丢失而静默放开。不可通过删状态卷或修改开关解除既有执行保护。

目录权威为 `license-core/catalog/catalog.go`（工作区路径）；本仓库构建使用审核过的
`third_party/license-core/catalog/catalog.go` 镜像。组件目录/发布批准与原业务授权均须满足。
部署、检查与恢复入口见[统一交付流程](../../platform/deploy/production/DELIVERY_WORKFLOW.md)。


The API and SLA notifier bind to `project_management` in compiled code. The
production Agent delivers a distinct `runtime/license-<compose-service>.env`
for each approved component; never copy credentials between services. Signed
state is persisted in that service's `/var/lib/commercial-license` volume and
the platform verification key is mounted read-only. Vendor trust is compiled
into the reviewed `third_party/license-core` consumer; no signing key is shipped.

Only never-enrolled components with no binding fields and no persisted runtime state
may use disabled compatibility mode; enrolled components cannot silently downgrade.
When `COMMERCIAL_LICENSE_ENABLED=true`, incomplete bindings fail startup;
missing/invalid signed state denies nonessential operations. A polling failure
does not extend the signed absolute expiry. Existing permission and data-scope
checks remain in force. Configuration and credential files are privileged
deployment assets, not an anti-root security boundary.

After expiry new project mutations, uploads and SLA notification generation
stop. Finite registered historical queries and exports remain available;
unknown routes default to business mutation. Background SLA, effect and notification
claims, side effects and acknowledgements recheck MUTATE_BUSINESS; expiry pauses
them. Authentication and diagnostic bootstrap remain available.

Run `sh scripts/license-core-sync.sh --check` before build. Independent checkouts
verify their exact mirror and checksum; workspaces additionally detect drift
from the authoritative core. Production approval requires a real immutable
image configuration digest, protocol label `com.basic-platform.license.protocol=1`
and a nonempty version built with `--build-arg APP_VERSION=<reviewed-version>`.
The protocol label describes signed-snapshot support, not component-registration V2.
Health/readiness alone is not migration eligibility or release approval.

## 操作步骤与验收

角色为部署管理员及平台商业许可read/manage管理员。受控登记 project-api、project-sla-notifier独立配置后，平台“运行时授权执行”选择项目服务管理，刷新、确认激活。成功须部署READY、ENFORCED、snapshot_current=true，两个当前成员完整当期ACK。兼容cmd/worker复用同一Runtime，不新增第三确认主体。

到期后新业务/上传/SLA新通知停止，既有授权仍存在且已生效、状态可信及原权限正常时保留有限历史查询和资质CSV导出，无额外写入宽限。资源页面 exportCapabilities → GET capabilities/export → 原 project.resource.read 与商业EXPORT_HISTORY → 应用查询、repository数据范围 → 内存CSV响应；不是任意新报告都能导出。SLA、派生事件和通知由独立Runtime调度，claim、外部投递和完成确认均复核许可与租约；通知配置未启用不能宣称送达。

失败先区分403业务权限与商业限制，查当前绑定/快照、独立卷、真实SLA_SCAN_TENANTS及通知配置，修复后走受控重试，不清卷或停全部服务。

依据：frontend project_management/api/projectManagement.js → internal/httpapi/api.go及commercial_license.go → internal/application/delivery.go → internal/infrastructure/mysql/repository.go及delivery.go；cmd/sla-notifier通过internal/background.Run装配独立Runtime。源码、本地和隔离验证见[任务记录](../../.ai/qa/project-temporal-bi-20261023/verification.md)；真实商业签名、自然ACK及正式服务器未验收。

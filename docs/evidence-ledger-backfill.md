# 证据台账历史记录盘点与回填方案（D1）

本文件是 D1 的只读盘点交付：先把 `pm_evidence_file` 存量绑定情况盘清楚，再给出受控回填路径。
本文件与 `cmd/evidence-ledger-inventory` 都**不写业务数据、不执行迁移、不提供自动写库脚本**。

## 1. 背景与边界

- 证据台账表 `pm_evidence_file` 的唯一键是 `(tenant_id, file_id)`，记录必须绑定到一个服务项
  `service_item_id`，并通过 `pm_service_item.project_id` 归到项目；`evidence_kind` 只允许
  `FIELD` / `DEVIATION` / `REPORT`（约束见 `migrations/000022_business_flow_controls.sql`）。
- 历史上可能存在三类异常：没有服务项级绑定、文件未绑定（`file_id` 为空）、绑定类型非预期
  （`evidence_kind` 不在允许集合，通常来自约束生效前的旧数据）。
- D1 只做盘点与方案：不修改 `internal/application/delivery_field.go`，不改表结构，不跑迁移，
  不写任何业务数据，`--apply` **不实现**（明确写“需单独立项”）。
- 本命令只允许只读 SELECT。连接配置与既有命令同源：读取 `MYSQL_DSN`
  （`cmd/migrate` 也是通过 `config.Load` 消费该变量）；不加载 OIDC/Temporal 等与盘点无关的必填项。
  建议给盘点单独签发只读数据库账号，不复用业务写账号。

## 2. 判定口径

| 口径 | 代码 | 判定条件 | 说明 |
| --- | --- | --- | --- |
| 缺少服务项级绑定 | `missing_service_item` | `TRIM(service_item_id) = ''` | 证据没有落到任何服务项 |
| 悬空服务项绑定 | `unknown_service_item` | `service_item_id` 非空，但同租户 `pm_service_item` 无该 `id` | 服务项被删/迁移/跨租户复用 |
| 文件未绑定 | `empty_file_id` | `TRIM(file_id) = ''` | 台账记录没有文件标识 |
| 绑定类型非预期 | `unexpected_kind` | `evidence_kind` 不精确属于 `FIELD`/`DEVIATION`/`REPORT` | 大小写、空格、旧枚举值都算异常 |
| 项目归属 | `project_id` | 经 `pm_service_item.project_id` 关联；无法关联时为 `''` | 仅用于分组，不判断项目状态 |

补充口径：

- 一条记录可同时命中多个口径：`flagged_records` 是按记录去重后的数量，`categories` 各项**可重叠**，
  不能把四项相加当作记录总数。
- 归档服务项（`pm_service_item.archived_at` 非空）视为已绑定，不计入异常；历史证据本身合法。
- 关联必须带 `tenant_id`：其它租户存在同 ID 服务项时按 `unknown_service_item` 处理，不能跨租户串绑定。
- 本盘点只判断“是否有绑定/绑定形状是否正确”，不判断归属是否正确，也不校验文件内容、摘要与文件网关一致性；
  这些需要业务侧逐条确认。

## 3. 盘点 SQL（只读 SELECT）

命令内部执行的就是以下两条 SELECT（使用占位符，不做字符串拼接）：

```sql
SELECT tenant_id, COUNT(*) AS records
FROM pm_evidence_file
-- 可选：AND tenant_id = ?
GROUP BY tenant_id
ORDER BY tenant_id ASC;

SELECT
  e.tenant_id,
  COALESCE(si.project_id, '') AS project_id,
  COUNT(*) AS flagged_records,
  COUNT(CASE WHEN TRIM(e.service_item_id) = '' THEN 1 END) AS missing_service_item,
  COUNT(CASE WHEN TRIM(e.service_item_id) <> '' AND si.id IS NULL THEN 1 END) AS unknown_service_item,
  COUNT(CASE WHEN TRIM(e.file_id) = '' THEN 1 END) AS empty_file_id,
  COUNT(CASE WHEN e.evidence_kind NOT IN ('FIELD','DEVIATION','REPORT') THEN 1 END) AS unexpected_kind,
  DATE_FORMAT(MIN(e.created_at), '%Y-%m-%dT%H:%i:%s.%fZ') AS earliest_created_at,
  DATE_FORMAT(MAX(e.created_at), '%Y-%m-%dT%H:%i:%s.%fZ') AS latest_created_at,
  SUBSTRING_INDEX(GROUP_CONCAT(e.id ORDER BY e.id SEPARATOR ','), ',', 5) AS sample_record_ids
FROM pm_evidence_file AS e
LEFT JOIN pm_service_item AS si
  ON si.tenant_id = e.tenant_id AND si.id = e.service_item_id
WHERE (
    TRIM(e.service_item_id) = ''
    OR (TRIM(e.service_item_id) <> '' AND si.id IS NULL)
    OR TRIM(e.file_id) = ''
    OR e.evidence_kind NOT IN ('FIELD','DEVIATION','REPORT')
  )
  -- 可选：AND e.tenant_id = ?
GROUP BY e.tenant_id, COALESCE(si.project_id, '')
ORDER BY e.tenant_id ASC, project_id ASC;
```

说明：

- `GROUP_CONCAT` 只取前 5 个记录 ID 作为定位样本，且 SQL 端已截断；大分组也不会把整表 ID 带进输出。
- 时间统一按库内 `created_at` 原值格式化输出（`%Y-%m-%dT%H:%i:%s.%fZ`），不换算时区。
- 第一条查询给“分母”，第二条给“异常按 tenant/project 统计”，两者必须来自同一个只读会话。

## 4. 命令用法

```sh
export MYSQL_DSN='<只读账号 DSN>'   # 与 cmd/migrate 同源的 MYSQL_DSN
export GOCACHE=$TMPDIR/dsh-go-build-cache GOFLAGS=-buildvcs=false

# 默认 dry-run + 表格输出，盘点全部租户
go run ./cmd/evidence-ledger-inventory

# 机器可读 JSON，供证据归档与回填前后对账
go run ./cmd/evidence-ledger-inventory --format json --tenant <tenant-id>

# 只盘点单个租户（可重复执行，输出稳定排序）
go run ./cmd/evidence-ledger-inventory --format table --tenant <tenant-id>
```

参数：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--format` | `table` | `table` 或 `json` |
| `--tenant` | 空（全部租户） | 只盘点指定租户；只用于占位符参数，不拼接 SQL |
| `--timeout` | `30s` | 只读查询超时，必须大于 0 |
| `--apply` | 未实现 | 直接拒绝并提示“回填需单独立项”，退出码 2 |

退出码：`0` 盘点成功；`2` 参数/配置错误（含 `--apply`）；`1` 连接或查询失败。任何 SQL 错误、
扫描错误、`rows.Err()` 都会向上返回并打印，不会降级为空结果或部分成功。

只读保障（命令内建，不依赖调用方自律）：

1. 连接后执行 `SET SESSION TRANSACTION READ ONLY`；设置失败直接终止，不在非只读会话盘点。
2. 连接池收敛为单连接（`MaxOpenConns(1)`），确保上述只读设置作用于后续所有查询。
3. SQL 只包含两条 `SELECT`，无 `INSERT/UPDATE/DELETE/DDL`；单测对 SQL 形状做静态断言。
4. 默认 dry-run；没有 `--apply` 实现，也不存在隐式写入口。

## 5. 输出字段与脱敏

输出只包含 tenant/project 标识、记录 ID、计数、类型口径与时间：

- 顶层：`mode`（固定 `DRY_RUN`）、`read_only`（固定 `true`）、`generated_at`、`tenant_filter`（可选）、
  `scanned.total/flagged`、`categories`、`tenants[]`、`groups[]`。
- `tenants[]`：`tenant_id`、`scanned_records`、`flagged_records`、`projects`、`categories`。
- `groups[]`：`tenant_id`、`project_id`、`flagged_records`、`categories`、`earliest_created_at`、
  `latest_created_at`、`sample_record_ids`（最多 5 个）。
- 明确不输出：`file_name`、`file_mime`、`file_size`、`file_sha256`、`created_by`、文件内容、
  客户/合同信息、DSN、账号口令或任何凭据。`MYSQL_DSN` 只用于 `sql.Open`，不会进入输出和错误汇总。

## 6. 回填方案（受控步骤，不提供自动写库脚本）

回填是**独立变更**，必须单独立项、单独审批、单独执行；本文档不提供 `UPDATE`/`DELETE`/`INSERT`
语句，也不提供批量脚本，避免被复制直接执行。

1. **备份**
   - 对目标库执行既有工程备份/快照流程，记录备份标识与校验值。
   - 至少完整备份 `pm_evidence_file`，并保证 `pm_service_item`、`pm_project` 在备份同一时点可还原。
2. **dry-run 计数（基线）**
   - 在只读账号上运行本命令，保存 `--format json` 输出（含 `generated_at`、`scanned`、`categories`、
     `groups` 与样本记录 ID）。
   - 该输出是回填前基线；回填后必须用同一口径、同一租户范围重跑对账。
3. **业务确认**
   - 按 tenant/project 逐组确认：异常记录的归属服务项、归属项目、证据类型、处理方式（补绑定/留档/作废）
     与依据（工单、交付记录、客户确认）。
   - 每条拟变更记录必须有业务责任人与审批记录；归属不明确的一律不进入执行批次。
4. **受控执行**
   - 由单独立项的变更单执行，按 tenant（必要时按 project、按记录 ID 批次）分批、小流量、可中断。
   - 执行前再次核对唯一键 `(tenant_id, file_id)` 冲突、`service_item_id` 存在性与
     `evidence_kind` 约束，避免把盘点异常转成写入失败。
   - 执行过程中保留操作人、时间、批次范围、变更前后计数的审计记录。
5. **审计与复核**
   - 回填后重跑本命令：预期异常计数按批次减少，且不产生新的未确认异常。
   - 对账差异必须登记为新的待确认项，不允许直接改数掩盖差异。
   - 将基线、执行记录、复核输出归档到证据台账；不要包含客户敏感字段或凭据。

## 7. 风险与回滚

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 服务项/项目归属判断错误 | 证据串项目、审计失真 | 业务逐条确认；按 tenant/project 批次；执行前复核归属 |
| 跨租户同 ID 误绑定 | 跨租户数据泄露 | 所有关联强制带 `tenant_id`；盘点把这种情况标成 `unknown_service_item` |
| `(tenant_id, file_id)` 唯一键冲突 | 回填写入失败或覆盖归属 | 执行前查冲突；失败即停，不跳过冲突继续跑 |
| `evidence_kind` 违反 CHECK 约束 | 回填中断 | 先按允许集合归一，再在变更单中确认取值 |
| 归档服务项被误判为可删/可改 | 历史证据丢失 | 归档服务项视为已绑定；回填不得物理删除服务项 |
| 大表全量扫描 | 影响业务库性能 | 只读账号；`--tenant` 限定范围；`--timeout` 限时；必要时在低峰期分租户执行 |
| 盘点输出泄露敏感信息 | 合规风险 | 输出字段白名单（仅 id/计数/类型/时间）；单测静态断言禁止字段 |

回滚：

- 以第 1 步的备份为准做整表/整库还原；跨批次回填需能按批次定位变更（批次内记录 ID 与操作时间）。
- 若使用受控反向变更单回滚，必须同样经过审批、备份、dry-run 计数与审计，不能临时手写脚本直接改库。
- 本命令不提供自动回滚；回滚方案与备份可用性需在变更单中提前演练/确认。

## 8. 验证

```sh
export GOCACHE=$TMPDIR/dsh-go-build-cache GOFLAGS=-buildvcs=false
cd project_management

go build ./...
go vet ./cmd/evidence-ledger-inventory/...
go test -count=1 ./cmd/evidence-ledger-inventory/...
```

单测只覆盖纯函数与 SQL/输出形状，不连接数据库；真实盘点需只读 DSN，由具备授权的操作人执行。
本次 D1 未连接任何真实数据库，也未执行迁移或写入。

## 9. 未覆盖事项

- 不判断证据归属是否正确、文件内容是否与摘要一致、文件网关是否仍可读取。
- 不对报告修订（`pm_report_revision`）与证据台账的一致性做交叉盘点，需另立盘点项。
- 不实现 `--apply`、批量回填、自动修复或任何写库命令；这些都属于后续单独立项。

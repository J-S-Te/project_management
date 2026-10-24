# 2026-10-22 项目复核修复交接

## 范围与实现

本记录对应父工程 `recheck/review.md` 的 R3/R4/R5、A04 缺编号兼容身份与 000033 DDL 中断重跑。按用户确定的方案，保留合同正常审批自动建项产品模式。本轮只写项目仓及前端项目模块，未修改根 `.ai`、其他业务仓、依赖清单或锁文件。两个仓均沿用 `develop/cxh` 的 dirty 工作树，既有改动保留。

- R3：新增项目级独立资格目录，按 `project.decomposition.manage` 和目标项目的既有数据范围读取项目，再筛选同客户、不同原合同、`approval_passed=true` 且 `approved/active` 的合同。已关联其他项目的合同仍可用作补充协议依据；新建项目目录仍独立维持原来的可建项规则。
- R4：调整提交的权威回读与候选目录复用同一资格校验；权威合同 ID/编号均与历史项目的 `contract_id`/`contract` 比较，拒绝原合同通过另一种身份形式作为补充协议。
- R5：拆解调整显式取得检测类别最低能力基线，与拆解结果合并归一后写入新项的 `RequiredCodes`。测试覆盖调整事件载荷，以及以该载荷建模确认后分配时对缺少 EXTRA 能力的拒绝；不声称该测试已验证真实数据库的调整→确认→分配全过程。
- A04：缺 `contract_number` 的投递在任何身份查找或盖章同步前受控回读权威合同；要求 ID 匹配、编号非空且审批通过。无法映射返回 503；命中项目的历史编号仍与权威编号冲突时返回前置条件失败。正常查重和并发唯一键冲突后的回读都保留相同检查，不猜身份、不重写或删除历史项目。
- 000033：本次未发布迁移仍先执行重复预检。之后核验同名索引的列、顺序、唯一性和前缀；先补唯一身份索引，再按需删除旧版本索引。支持旧索引、两个索引并存、DDL 已完成但未记账三种重跑状态；定义漂移失败关闭。旧迁移 000024 未编辑。
- 前端：调整弹窗使用独立目录状态及加载序号；项目切换或旧响应迟到时不打开/覆盖旧目录，目录失败清空旧候选，提交时再次核对目标项目。候选额外复核客户、历史原编号与审批状态。

## 接口变化

`GET /api/v1/projects/:id/supplement-contracts?limit=200`

- 项目会话；权限 `project.decomposition.manage`，无需 `project.create`；沿用目标项目授权范围。
- `limit` 可省略，范围 1–200；非法值 422，权限/范围无授权 403，不可见/不存在项目 404，合同服务不可用 503。
- 返回现有 `ApprovedContract[]` 摘要 DTO：ID、编号、标题、客户、版本、状态、审批标记；不新增价格/正文/附件字段。
- 商业授权分类为 READ_HISTORY，仍执行会话、业务权限及数据范围门禁。
- 前端入口 `listSupplementContracts(projectID, params)`。
- 兼容激活入口的成功响应格式不变，但缺编号又无可核验权威源时不再成功；历史身份冲突也不再盖章同步。

目录沿用合同端最多 200 条摘要的读取能力，客户过滤发生在项目端；未扩展合同端的按客户分页协议。因此超过该上游窗口的旧合同可发现性仍是边界，主 Agent 如需扩大应协调合同端协议，不把此处目录称为全量合同检索。

## 修改文件（仅本轮增量；各文件还可能含既有 dirty 改动）

项目仓：

- `internal/application/delivery.go`
- `internal/application/supplement_catalog.go`（新增）
- `internal/application/project_counterexample_test.go`（新增，正式纳入原 overlay 反例）
- `internal/httpapi/api.go`
- `internal/httpapi/api_test.go`（成功路径 fixture 补权威编号，保留原断言）
- `internal/httpapi/commercial_license.go`
- `internal/httpapi/commercial_route_policy_test.go`
- `internal/httpapi/supplement_catalog_test.go`（新增）
- `internal/infrastructure/mysql/contract_identity_native_test.go`（新增）
- `migrations/000033_contract_project_identity.sql`
- `migrations/migrations_test.go`
- `migrations/identity_resume_mysql_test.go`（新增、显式选择才执行）
- 本交接记录。

前端项目模块：

- `api/projectManagement.js`
- `api/supplementCatalog.test.mjs`（新增）
- `views/ProjectManagementView.vue`
- `views/supplementCatalog.test.mjs`（新增）

## 真实红绿证据

Go 原生命令统一使用以下包装（未加载业务运行配置，依赖离线；仓内缓存避免默认缓存写权限限制）：

```sh
go_native() {
  env -u PM_TEST_DSN -u PM_CAPABILITY_IMPORT_TEST_DSN \
    -u PM_HANDOVER_TEST_MYSQL_DSN -u PM_IDENTITY_MIGRATION_TEST_DSN \
    -u MYSQL_DSN -u DATABASE_URL -u TEST_MYSQL_DSN \
    GOPROXY=off GOTOOLCHAIN=local GIN_MODE=release \
    GOCACHE=/Users/yglf/GOPATH/src/Unified_Identity_Authentication_Platform/project_management/.gocache \
    /Users/yglf/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.darwin-arm64/bin/go "$@"
}
```

在项目仓执行的关键验红命令与实际失败：

- `go_native test ./internal/application -run '^TestReviewLegacyOriginalContractMustNotBecomeItsOwnSupplement$' -count=1`：FAIL，原合同经稳定 ID 被接受。首次默认缓存运行另有 setup failed，修正缓存后才取得这里的业务失败；不将 setup failed 当红阶段。
- `go_native test ./internal/application -run '^TestReview' -count=1`：当时四个反例 FAIL，分别为原合同、调整项空能力基线、缺编号且无权威源成功、未回读权威编号。
- `go_native test ./internal/httpapi -run '^TestSupplementCatalog' -count=1`：FAIL，新路由 404。
- `go_native test ./migrations -run '^TestContractProjectIdentityMigrationCanResumeAfterDDLBeforeLedger$' -count=1`：FAIL，缺 information_schema 索引恢复护栏。这是结构检查，非真实 MySQL 中断实验。
- `go_native test ./internal/httpapi -run '^TestExpiredProjectExactHandoverAndAuditPolicies$/GET/api/v1/projects' -count=1`：FAIL，新增 GET 被分类为 MUTATE_BUSINESS；登记只读分类后通过。
- `go_native test ./internal/application -run '^TestReviewCompatibilityActivationMustNotGuessLegacyIdentityFromID$' -count=1`：FAIL，无权威源时命中旧编号并同步。
- `go_native test ./internal/application -run '^TestReviewCompatibilityAuthorityMismatchAfterConcurrentInsert$' -count=1`：FAIL，并发冲突回读分支接受歧义身份；补齐同样门禁后通过。
- 前端仓 `node --test src/modules/project_management/views/supplementCatalog.test.mjs`：初始 3 FAIL（未读取独立目录、迟到响应、失败关闭），实现后转绿。

后续追加的 HTTP 错误、SQLite 范围与前端 DTO/候选校验等为补充验证，不声称每个用例均独立经历了红阶段。正式测试已纳入默认套件，原只读 recheck 文件保留，无需再用 overlay 重复注入同名函数。

## 验证与交接门禁

- 最后一次 `go_native test ./... -count=1` 退出 0：项目全包默认套件通过。DSN 相关集成测试按条件跳过；包含内存 SQLite 的真实仓储身份查询和目录租户/组织范围测试，不连接 MySQL。
- `go_native vet ./internal/application ./internal/httpapi ./internal/infrastructure/mysql ./migrations` 退出 0。
- 前端模块 `node --test src/modules/project_management/*.test.mjs src/modules/project_management/api/*.test.mjs src/modules/project_management/views/*.test.mjs`：167 PASS，0 FAIL/SKIP。
- 前端完整 `npm test -- --test-reporter=dot` 较早一次退出 0，发现 163 个文件；最终共享工作树复跑退出 1。随后以 TAP 摘要确认 1171 tests：1168 PASS、3 FAIL、0 SKIP，不能把较早结果当作最终全前端通过。
- 前端 `npm run build -- --outDir /private/tmp/project-recheck-build.sSyoIo` 退出 0（287 modules）；保留 outDir 位于工程外及 >500kB chunk 提示，输出为临时构建产物。
- 两仓本任务范围 `git diff --check` 通过。

最终完整前端的范围外失败（本轮未修改这些文件）：

- `src/modules/customer-opportunity-ui.test.mjs:648`：商机类型和来源使用租户目录、结构化 ID 与安全删除；旧 `Confirm & Auto Close` 文案断言不匹配。
- `src/modules/customer-opportunity-ui.test.mjs:1253`：TS-004 详情以权威操作接口和稳定游标驱动且安全展示时间线。
- `src/modules/customer_portal/api/portal-capabilities.test.mjs:39`：Portal views load capabilities once and fail closed dependency-backed actions；旧 `Keep dependency-backed actions closed` 注释断言不匹配。

复核命令为 `npm test -- --test-reporter=tap`，通过 `set -o pipefail` 和只输出 not-ok/统计行的 awk 保留退出码。项目模块 167/167 不受这些范围外断言失败影响；由主 Agent 处理跨模块共享工作树集成。

本轮不执行 MySQL、外部 worker、浏览器、Temporal 或真实合同系统验收。主 Agent 可在专用空 schema（名称必须以 `_identity_resume_test` 结尾）上显式设置 `PM_IDENTITY_MIGRATION_TEST_DSN`，运行：

```sh
go test ./migrations -run '^TestContractIdentityDDLInterruptedBeforeLedgerOnMySQL$' -count=1 -v
```

该测试仅消费显式隔离 DSN，拒绝非空 schema；创建并清理自己拥有的测试表/过程，覆盖上述三种恢复状态、重复历史数据、同名非唯一索引漂移，重跑并确认行数不变。默认原生套件对其 SKIP，不能算 MySQL PASS。

发布前须确认目标库是否已记账旧版 000033：本次按用户明确的“未发布 000033”范围修订；若某环境已应用，不能改账或绕过 checksum，应由主 Agent 制定后续兼容迁移。存量身份冲突只读预检与人工处理、跨仓集成、隔离 MySQL 及外部业务验收均由主 Agent 接续。未提交、推送、部署或操作业务数据库。

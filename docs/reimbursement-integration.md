# 报销费用引用与现金支出投影

此能力默认关闭；接收方只能读取已验签的租户及调用环境，两个用途分别使用精确 ClientID。读取 scope 为 `reimbursement.project_reference.read`，写入/对账/重建 scope 为 `project.reimbursement_cost.write`。配置变量及 purpose 见 `.env.example`。purpose 是平台注册元数据，不当作 JWT claim。`OIDC_TENANT_ID` 固定目标租户；不会继承结算的任意客户端主体选项。

权限目录版本11：`expense_participant` 只含 `project.expense_reference.eligible`，不含 `project.read`；新增 `sales_director` 同样仅含该最小引用权限，用于销售负责人目录，不授予交付/报告操作。费用参与名单管理另需 `project.expense_participant.manage` 与当前项目范围；项目现金支出摘要另需 `project.reimbursement_spend.read` 与当前项目范围。人员来源为当前平台应用 owner-directory；请求人失效、目录不可用或非唯一精确主体均失败关闭。

## 费用引用

- `POST /internal/v1/reimbursement/project-references/search`：`actor_subject_id,keyword,cursor,page_size`（1..50，默认20）。返回 `data.items` 及可选 `next_cursor`。
- `POST /internal/v1/reimbursement/project-references/batch-validate`：`actor_subject_id,claim_id,draft_version,references[{project_id,service_item_id,occurred_date,approval_org_id?}]`，最多100项。费用日期严格为 `YYYY-MM-DD`。返回相同最小引用及草稿绑定字段。
- `GET/PUT /api/v1/projects/:id/expense-participants`：读取或带 `subject_id,status,valid_from,valid_until,reason,expected_version` 更新。名单最多有效366天，撤销仍需正确版本；新增为 expected_version=0。版本冲突409，审计与名单同事务提交。

资格来自项目 owner/manager、指定服务项当前 team_lead/project_manager/engineer、有效显式名单。仅服务项A工程师不能选择项目级或服务项B；项目级负责人或显式名单可按项目级关联。NATIVE不强制合同ID。归档报告不剥夺引用资格；被替换归档的服务项不参与新引用。终止项目/服务项仍 `eligible=false`，当前资格有效时保留 `participant_eligible=true` 的最小引用，历史例外由报销侧受控政策决定。审批目标缺失单独放在 `approver_reason_codes[node_role]`，不混入参与资格 `reason_codes`，消费方只对本次路线必需节点执行 `APPROVER_MISSING`。

审批引用明确 `node_role`：`PROJECT_EXPENSE_OWNER` 只取服务项 `project_manager_id` 或项目级 `manager_identity_id`，不回退团队负责人或owner，并精确复核当前 `project_manager` 角色及有效任职组织（不要求经理与项目同部门）；技术总监按项目 `OwnerOrgID` 的当前 `technical_director` 角色目录解析；销售总监按后端员工费用档案组织 `approval_org_id` 的当前 `sales_director` 角色解析。该销售组织必须仍在申请人当前有效目录组织中，不能借项目技术组织替代；浏览器不得指定此字段，报销后端从已发布档案派生并绑定批验集合。search无费用组织时只返回经理/技术目标和销售节点缺上下文提示，不猜销售主组织。

经理保留真实对象 `source_version`；总监目录没有数字授权revision，明确 `source_version=0`，使用 `source_field=role:technical_director/role:sales_director`、真实组织 `source_org_id` 及规范角色查询/组织结果SHA256 `source_digest`。项目自身可信应用/环境目录执行角色过滤，不用报销本地业务映射。完整分页最多500名角色用户，每个角色组织最多20个目标，多目标全部返回并由报销按ALL处理；分页或目录故障失败关闭，不返回截断目标。节点目标、实际组织及来源摘要都进入 `reference_version`。搜索/批验每个请求独立缓存精确用户/角色/页查询以保持同批集合一致；新请求重新查询，引用有效期不晚于请求开始后两分钟，也不延长更早的显式资格期限。

权威返回包含版本、资格来源、责任引用与最短两分钟有效窗口。申请人当前应用授权经目录重查；资格有效期按当前提交时刻判断。费用标准、报销期间及历史费用例外由报销侧管理，不把服务项计划时间当作未经确认的费用日期硬限制。机器请求拒绝任意租户、角色、范围及未知字段。

## 支出协议

`POST /internal/v1/reimbursement/spend-snapshots` 只接收第6章完整不可变付款效果+当前贡献。精确 Go DTO 与规范哈希函数在 `internal/domain/reimbursement_spend.go`。金额均为规范十进制 BIGINT 字符串，首发CNY；时间为UTC RFC3339整秒（`...Z`）。无服务项使用 JSON null。每个效果有 `payment_record_id,occurred_at,recorded_at,approved_content_hash,allocations`，冲正必须逐项完整反向对应唯一原付款。

规范摘要排除 `event_id/snapshot_hash`，固定 Go结构字段顺序；effects按effect_id、allocations/contributions按 `project_id + NUL + service_item_id + NUL + fee_type` 排序。事件哈希另覆盖完整冻结载荷。限制1MiB、2000累计效果、1000累计效果分摊、1000贡献；超限拒绝，不截断历史。

迁移000045新增 inbox、cursor、不可变snapshot、effect、allocation、projection及名单、修复审计、冲突隔离表。单事务锁定单据游标并保存全部证据、替换整单贡献和冻结回执，提交后才返回成功。相同事件精确重放返回原回执；新事件但同版本同摘要返回DUPLICATE；旧版返回SUPERSEDED且不回退金额。同事件异载荷、同版本异hash、效果业务键冲突、完整历史遗漏与错误归属均拒绝。已终止项目仍接收合法历史付款和冲正，归档历史服务项通过同租户同项目关系验证。

## 对账与修复

- `GET /internal/v1/reimbursement/spend-snapshots/reconciliation?cursor=<opaque_updated_at_claim_cursor>&page_size=20`，最大100，响应含 next_cursor，按更新时间及单据复合游标增量读取。返回 `claim_id,projection_revision,snapshot_hash,currency,paid_net_minor,updated_at,consistent`。不仅比较cursor摘要，还比较实际贡献、效果载荷/哈希、每行allocation。
- `POST /internal/v1/reimbursement/spend-snapshots/rebuild`，body `claim_id,reason`。专用支出身份按同一租户/环境锁定游标，从已校验不可变快照重建贡献，修复理由同事务保存。原事实错误仍需来源新增更正版本；不提供改累计金额入口。
- `GET /api/v1/projects/:id/reimbursement-spend?cursor=<claim_id>&page_size=20` 输出单据净额及版本摘要；现金净支出口径不能直接称会计成本或利润。

## 验证界限

默认使用既有隔离SQLite验证事务不变量。真实MySQL测试只接受显式 `PM_REIMBURSEMENT_TEST_DSN`：全新空库、库名前缀 `pm_reimbursement_test_`、localhost高位端口（拒绝3306）；未配置即跳过，不探测业务库。尚未在真实环境启用机器配置、部署或执行数据迁移。商业许可保持精确路由分类：引用POST为READ_HISTORY；付款消费和重建为MUTATE_BUSINESS，不绕过运行门禁。

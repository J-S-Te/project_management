# 主报告整改状态升级说明

本文件是发布说明，编写此说明不代表已发布。当前实现未提交、未部署。

## 行为与数据

- 主报告链：NONE → RECTIFYING → COMPILING → REVIEWED → ISSUED → ARCHIVED。
- NONE 表示待整改，现场完成不会自动进入编制。整改和编制共用 project.report.prepare；权限目录版本仍为8。
- 整改归入项目级报告编制rank7，前端报告局部进度每阶段20%。
- 存量编制/审核/签发/归档记录的字符串值及修订证据不变，无需数据回填。
- 已签发/归档更正批准后回到新修订NONE；整改返工保留旧修订并生成新修订NONE。
- 内嵌渗透专项仍为NONE→DRAFTING→SUBMITTED→APPROVED→ISSUED→ARCHIVED。

## 迁移与恢复

新迁移为000041_report_rectifying.sql，000040原生来源迁移属于其他任务，须核对实际目标库版本。
000041只扩展修订status CHECK，不改历史行；保留DRAFTING/SUBMITTED/APPROVED和全部原状态。
迁移校验既有约束定义及启用状态：旧定义替换为新定义，新定义允许安全重跑，缺失约束可重新添加；
未知或未启用的同名约束停止处理。添加约束时不合法存量会阻止DDL，不以改业务数据方式强行放行。
DDL完成而迁移账本尚未写入时，重新运行迁移会核验新定义后补齐账本。

## 受控发布顺序

1. 获得具体环境发布授权，按工程既有规则完成备份并确认成功，保留旧镜像和配置恢复材料。
2. 暂停报告写入和旧版本写实例，按既有后台prepare/drain流程操作，避免新旧状态规则混跑。
3. 先执行匹配新代码的项目迁移并核对000041记账、CHECK定义和待迁移清单。
4. 更新项目API及相关后台组件，再更新匹配的统一前端；恢复写入前核对健康和版本。
5. 最小发布后验证：完整报告链、NONE直编制拒绝、整改返工；按真实权限完成，保留证据。

现有本地受控入口是在platform目录执行 `bash scripts/docker-local.sh refresh-project-api`，
该入口构建项目API/后台、执行project-migrate、处理后台受控切换；统一前端使用既有refresh-frontend入口。
`refresh-api`只更新基础平台，不会更新项目后端。上述命令仅供后续已授权操作参考，本任务没有执行。

## 回退边界

旧二进制不认识RECTIFYING，旧前端没有对应后继，旧API仍允许NONE直编制。
因此新增状态写入后，不能把直接回滚旧二进制视为安全恢复，也不要收紧CHECK删除RECTIFYING。
优先停写并恢复匹配的新版本；确需回退旧规则时，必须另行制定并授权处理已进入整改的记录。
不得静默把整改记录改成NONE或COMPILING以绕过业务过程。

## 本轮验证范围

本轮在独立本机MySQL8.0.46执行迁移正常升级/重跑/约束恢复，以及主报告链和整改返工最小验证。
前端以真实计算函数及模板SSR验证五步链、动作入口、颜色和20%进度；构建独立执行。
本轮没有重建现行业务容器，没有执行生产MySQL8.4、真实登录浏览器或部署后业务验收。
详细结果和工作区全量测试的其他任务状态见工程 `.ai/qa/report-rectifying-20261104/`。

## 项目级报告阶段 report_stage（本波次新增）

- 项目状态仍是 9 节点线性枚举（按"取最滞后服务项阶段"派生）；report_stage 是与之**正交**的派生维度，不是第 10 个状态节点，也不落库。
- 唯一事实来源在 domain：`ReportPhaseStage{Code,Phase,LifecycleRank}` 表定义 NONE(0,6)、RECTIFYING(1,7)、COMPILING(2,7)、REVIEWED(3,7)、ISSUED(4,7)、ARCHIVED(5,8)。SQL 分支、SLA 阶段目录、等级表 `ServiceItemReportStages` 均由该表派生，基础设施层不再手写清单。
- 取值：NONE/RECTIFYING/COMPILING/REVIEWED/ISSUED/ARCHIVED；`""` 表示未进入报告段或不可判定（无服务项、全部终止、仍有服务项未到现场实施完成、出现未知报告码）。
- 不变量（测试守护）：
  - 现场实施完成 ⇒ NONE 或空；
  - 报告编制 ⇒ RECTIFYING..ISSUED；
  - 已完成 ⇒ ARCHIVED；
  - 其余线性节点（未到现场完成）⇒ 空；
  - 补充协议处理中不短路本维度（分支节点由 status 承载）。
- 派生取**最小** Phase（最滞后），明确否决"取最靠前"：一个服务项待整改、另一个整改中时项目停在 NONE。未知报告码返回空并由 `UnknownReportPhases` 显式暴露，不静默归入 NONE。
- 列表 API `GET /projects` 新增 `report_stage`（大小写不敏感，只接受 6 个码，非法值 422）；与 `status` 可 AND 组合、共用一次服务项聚合 join。既有 `status` 刻意保持宽松（未知值返回空集），避免打断历史调用方。
- 服务项聚合 join 增加 `archived_at IS NULL`（与 Go 侧 GORM 软删除一致），修复归档服务项污染项目派生状态的既有缺陷。

## 监控与 SLA 增量

- `GET /projects-monitoring` 接受 `report_stage`，语义与列表一致；每行项目内嵌 `project.report_stage`。
- 快照新增 `report_stage_counts`（按派生报告阶段对全筛选集分组）。既有 `status_counts` 同步改为按**派生**状态表达式分组，不再按存储列 `pm_project.status` 分组（与行口径对齐，属既有缺陷修复）。
- SLA 规则目录新增 5 个报告阶段码（整改中/编制中/审核中/已签发/已归档），供运营配置阈值；不写种子数据、不进迁移。
- 报告阶段停留以 `pm_service_item.report_updated_at` 计时（每次报告推进刷新），匹配 `report_status`；状态规则仍用 `status_changed_at`。命中条目带 `report_stage` 与 `rule_dimension`，并沿用既有 `slaProjects` 逻辑进入 sla/risk 筛选。
- 通知幂等键改为 `sla-{itemID}-{kind}-{dimension}-{ruleStatus}-{bizday}`：同一服务项同一天同时命中状态规则与报告阶段规则时两条通知都会送达；报告阶段文案使用「报告·整改中」等阶段名。
- `monitoringMilestone` 与 `action_section` 未改动，前端既有展示不变。

## 迁移

本波次零新迁移：复用既有 `pm_service_item.report_status` / `report_updated_at`（000008 起存在）。不新增 `pm_project` 列，不改变 BI 快照契约，`report_stage` 只读派生。

## 受控发布顺序（本波次）

1. 先发布项目后端：report_stage 是新增响应字段，旧前端忽略即可，无破坏性变更。
2. 旧前端在新后端上无害：未传 `report_stage` 即不过滤；新增 `report_stage_counts` 与 `report_stage` 字段不会被旧页面使用。
3. 前端本波次同步接入：列表/看板/监控徽章与 `report_stage` 筛选、监控“整改中”页签与分桶、首页服务项分布“其中整改中 N 项”；旧后端缺少字段时不渲染徽章并忽略参数。
4. BI 快照接入 report_stage 不在本波次范围，保持现有契约不变。

## 本轮验证范围（report_stage）

- domain 单测覆盖报告阶段表、派生矩阵与不变量；mysql 单测用 DryRun 断言 join 只加一次、`archived_at IS NULL`、`lowest_report_phase`、绑定顺序与监控计数分组。
- 真实 MySQL 集成（`PM_TEST_DSN`，未设置时跳过）：报告阶段分页等价性（含归档项与整改中兄弟项）、双筛选组合、监控计数=行派生、报告阶段 SLA 进入 sla/risk 筛选。
- 本轮后端未执行浏览器验收与部署；前端 `pnpm test` 2211 通过、`pnpm build` 成功（详见 `.ai/qa/report-stage-visibility-20261105/verification.md`）；BI 未接入。

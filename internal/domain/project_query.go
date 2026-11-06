package domain

// ProjectListQuery 是项目列表的服务端查询契约。筛选条件由服务端校验后
// 统一下推仓储层（SQL COUNT + LIMIT/OFFSET），HTTP 层不得自行拼装 SQL 片段。
//
// ReportStage 与 Status 刻意不对称：Status 保持宽松（未知值返回空集，
// 兼容历史调用方与存量前端），ReportStage 是新增的受限词表，只接受
// ReportPhaseStages 中的 6 个码，非法值由应用层返回 422，避免前端
// 参数拼错时静默退化成"不过滤"。
type ProjectListQuery struct {
	// Keyword 是 id/名称/客户/合同/类别/经理的模糊匹配关键字。
	Keyword string
	// Status 是派生的项目状态过滤值（宽松，未知值等价于空集）。
	Status string
	// ReportStage 是项目级报告阶段过滤值，必须是已知报告阶段码（大小写不敏感）。
	ReportStage string
	// Page / PageSize 是分页参数；PageSize<=0 表示不分页（内部全量调用）。
	Page     int
	PageSize int
}

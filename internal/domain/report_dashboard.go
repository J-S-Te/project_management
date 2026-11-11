package domain

// ReportDashboard 是租户报告版本台账的权威聚合快照。
type ReportDashboard struct {
	Available       bool                   `json:"available"`
	TenantID        string                 `json:"tenant_id"`
	SnapshotAt      string                 `json:"snapshot_at"`
	SourceUpdatedAt string                 `json:"source_updated_at,omitempty"`
	SourceCode      string                 `json:"source_code"`
	Metrics         ReportDashboardMetrics `json:"metrics"`
	MissingMetrics  []string               `json:"missing_metrics"`
	Basis           string                 `json:"basis"`
}

type ReportDashboardMetrics struct {
	ActiveReports   int64            `json:"active_reports"`
	VoidReports     int64            `json:"void_reports"`
	StatusCounts    map[string]int64 `json:"status_counts"`
	CycleP50Days    *float64         `json:"cycle_p50_days"`
	CycleSamples    int64            `json:"cycle_samples"`
	ReviewedReports int64            `json:"reviewed_reports"`
}

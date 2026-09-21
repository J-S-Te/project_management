package domain

// PenetrationWorkPackage is an optional penetration-testing engagement embedded in an
// equal-protection service item. It is deliberately not a ServiceItem: project progress,
// contract decomposition and settlement continue to count only the parent service item.
type PenetrationWorkPackage struct {
	ID                   string   `json:"id"`
	ProjectID            string   `json:"project_id"`
	ParentServiceItemID  string   `json:"parent_service_item_id"`
	DecisionStatus       string   `json:"decision_status"`
	CustomerContact      string   `json:"customer_contact,omitempty"`
	CommunicatedAt       string   `json:"communicated_at,omitempty"`
	CommunicationSummary string   `json:"communication_summary,omitempty"`
	DecisionChangeReason string   `json:"decision_change_reason,omitempty"`
	PlannedStart         string   `json:"planned_start,omitempty"`
	PlannedEnd           string   `json:"planned_end,omitempty"`
	EngineerIDs          []string `json:"engineer_ids"`
	AuthDocNo            string   `json:"auth_doc_no,omitempty"`
	AuthStart            string   `json:"auth_start,omitempty"`
	AuthEnd              string   `json:"auth_end,omitempty"`
	AuthScope            string   `json:"auth_scope,omitempty"`
	TestScope            string   `json:"test_scope,omitempty"`
	TestWindow           string   `json:"test_window,omitempty"`
	EmergencyContact     string   `json:"emergency_contact,omitempty"`
	RollbackPlan         string   `json:"rollback_plan,omitempty"`
	ExecutionStatus      string   `json:"execution_status"`
	ReportStatus         string   `json:"report_status"`
	ReportRevision       uint64   `json:"report_revision"`
	Version              uint64   `json:"version"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
	UpdatedBy            string   `json:"updated_by"`
}

type EnsurePenetrationWorkPackageInput struct {
	ExpectedVersion uint64 `json:"expected_version"`
	IdempotencyKey  string `json:"-"`
}

type PenetrationDecisionInput struct {
	DecisionStatus       string `json:"decision_status"`
	CustomerContact      string `json:"customer_contact"`
	CommunicatedAt       string `json:"communicated_at"`
	CommunicationSummary string `json:"communication_summary"`
	ChangeReason         string `json:"change_reason"`
	ExpectedVersion      uint64 `json:"expected_version"`
	IdempotencyKey       string `json:"-"`
}

type PenetrationPlanInput struct {
	PlannedStart     string   `json:"planned_start"`
	PlannedEnd       string   `json:"planned_end"`
	EngineerIDs      []string `json:"engineer_ids"`
	AuthDocNo        string   `json:"auth_doc_no"`
	AuthStart        string   `json:"auth_start"`
	AuthEnd          string   `json:"auth_end"`
	AuthScope        string   `json:"auth_scope"`
	TestScope        string   `json:"test_scope"`
	TestWindow       string   `json:"test_window"`
	EmergencyContact string   `json:"emergency_contact"`
	RollbackPlan     string   `json:"rollback_plan"`
	ExpectedVersion  uint64   `json:"expected_version"`
	IdempotencyKey   string   `json:"-"`
}

type PenetrationExecutionInput struct {
	Action               string `json:"action"`
	CustomerContact      string `json:"customer_contact,omitempty"`
	CommunicatedAt       string `json:"communicated_at,omitempty"`
	CommunicationSummary string `json:"communication_summary,omitempty"`
	Reason               string `json:"reason,omitempty"`
	ExpectedVersion      uint64 `json:"expected_version"`
	IdempotencyKey       string `json:"-"`
}

type PenetrationReportStatusInput struct {
	Phase           string `json:"phase"`
	ExpectedVersion uint64 `json:"expected_version"`
	IdempotencyKey  string `json:"-"`
}

type PenetrationReportArtifactInput struct {
	ReportArtifactInput
	ExpectedVersion uint64 `json:"expected_version"`
	IdempotencyKey  string `json:"-"`
}

type PenetrationWorkPackageStats struct {
	Total           int64 `json:"total"`
	PendingDecision int64 `json:"pending_decision"`
	Required        int64 `json:"required"`
	InProgress      int64 `json:"in_progress"`
	Completed       int64 `json:"completed"`
}

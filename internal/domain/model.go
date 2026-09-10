package domain

import "time"

type Project struct {
	TenantID          string    `json:"-"`
	OwnerOrgID        string    `json:"owner_org_id,omitempty"`
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Customer          string    `json:"customer"`
	Contract          string    `json:"contract"`
	ContractVersion   string    `json:"contract_version,omitempty"`
	SupplementStatus  string    `json:"supplement_status,omitempty"`
	Services          int       `json:"services"`
	Category          string    `json:"category"`
	Team              string    `json:"team"`
	Manager           string    `json:"manager"`
	OwnerIdentityID   string    `json:"owner_identity_id,omitempty"`
	ManagerIdentityID string    `json:"manager_identity_id,omitempty"`
	Health            string    `json:"health"`
	Status            string    `json:"status"`
	Progress          int       `json:"progress"`
	Due               string    `json:"due"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ServiceItem struct {
	TenantID          string   `json:"-"`
	ID                string   `json:"id"`
	ProjectID         string   `json:"project_id"`
	SourceServiceID   string   `json:"source_service_id,omitempty"`
	Batch             string   `json:"batch"`
	Site              string   `json:"site"`
	Category          string   `json:"category"`
	Requirement       string   `json:"requirement"`
	System            string   `json:"system"`
	SystemLevel       string   `json:"system_level"`
	Special           string   `json:"special"`
	TestMode          string   `json:"test_mode"`
	TeamLeadID        string   `json:"team_lead_id,omitempty"`
	ProjectManagerID  string   `json:"project_manager_id,omitempty"`
	EngineerIDs       []string `json:"engineer_ids,omitempty"`
	EquipmentIDs      []string `json:"equipment_ids,omitempty"`
	RequiredCodes     []string `json:"required_codes,omitempty"`
	PlannedStart      string   `json:"planned_start,omitempty"`
	PlannedEnd        string   `json:"planned_end,omitempty"`
	ConflictStatus    string   `json:"conflict_status,omitempty"`
	TechReviewStatus  string   `json:"tech_review_status,omitempty"`
	TechReviewedAt    string   `json:"tech_reviewed_at,omitempty"`
	TechReviewedBy    string   `json:"tech_reviewed_by,omitempty"`
	TechReviewComment string   `json:"tech_review_comment,omitempty"`
	ReportStatus      string   `json:"report_status,omitempty"`
	ReportUpdatedAt   string   `json:"report_updated_at,omitempty"`
	ReportUpdatedBy   string   `json:"report_updated_by,omitempty"`
	Status            string   `json:"status"`
	ImplementationPlan *ImplementationPlan `json:"implementation_plan,omitempty"`
}

type Rule struct {
	TenantID string `json:"-"`
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Trigger  string `json:"trigger"`
	Enabled  bool   `json:"enabled"`
	Updated  string `json:"updated"`
	Notes    string `json:"notes,omitempty"`
	UpdatedBy string `json:"-"`
	// CheckType / Threshold 用于预警规则。
	CheckType string `json:"check_type,omitempty"`
	Threshold string `json:"threshold,omitempty"`
	// Target 用于自动化动作目标。
	Target string `json:"target,omitempty"`
	// RoleCode / FieldName / AccessLevel 用于字段级权限。
	RoleCode    string `json:"role_code,omitempty"`
	FieldName   string `json:"field_name,omitempty"`
	AccessLevel string `json:"access_level,omitempty"`
	// Status / DeadlineHours / RemindHours 用于 SLA 规则。
	Status        string `json:"status,omitempty"`
	DeadlineHours int    `json:"deadline_hours,omitempty"`
	RemindHours   int    `json:"remind_hours,omitempty"`
}

type Snapshot struct {
	Projects     []Project     `json:"projects"`
	ServiceItems []ServiceItem `json:"service_items"`
	Rules        []Rule        `json:"rules"`
}

type Dashboard struct {
	TenantID         string         `json:"tenant_id,omitempty"`
	ProjectCount     int            `json:"project_count"`
	InFlightProjects int            `json:"in_flight_projects"`
	RiskProjects     int            `json:"risk_projects"`
	ServiceItems     int            `json:"service_items"`
	StatusCounts     map[string]int `json:"status_counts"`
}

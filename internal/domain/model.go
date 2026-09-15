package domain

import (
	"errors"
	"strings"
	"time"
)

type Project struct {
	TenantID          string    `json:"-"`
	OwnerOrgID        string    `json:"owner_org_id,omitempty"`
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Customer          string    `json:"customer"`
	CustomerID        string    `json:"customer_id,omitempty"`
	Contract          string    `json:"contract"`
	ContractID        string    `json:"contract_id"`
	ContractVersion   string    `json:"contract_version,omitempty"`
	SupplementStatus  string    `json:"supplement_status,omitempty"`
	Services          int       `json:"services"`
	Category          string    `json:"category"`
	Team              string    `json:"team"`
	Manager           string    `json:"manager"`
	OwnerIdentityID   string    `json:"owner_identity_id,omitempty"`
	ManagerIdentityID string    `json:"manager_identity_id,omitempty"`
	Status            string    `json:"status"`
	Progress          int       `json:"progress"`
	Due               string    `json:"due"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`

	// Risk 是服务端派生的风险标记：派生状态为异常处理中/已终止，或存在已终止服务项。
	// 由服务端统一计算，前端不得复刻该口径（此前前后端各写一遍，口径调整即不一致）。
	Risk bool `json:"risk"`
	// Version 是项目业务版本：读取方据此识别"我看到的是不是最新一版"，
	// 写入方可带上期望版本做条件更新（不匹配即 409 冲突）。
	Version uint64 `json:"version"`
}

type ServiceItem struct {
	TenantID           string              `json:"-"`
	ID                 string              `json:"id"`
	ProjectID          string              `json:"project_id"`
	SourceServiceID    string              `json:"source_service_id,omitempty"`
	Batch              string              `json:"batch"`
	Site               string              `json:"site"`
	SiteCode           string              `json:"site_code,omitempty"`
	Category           string              `json:"category"`
	Requirement        string              `json:"requirement"`
	System             string              `json:"system"`
	SystemLevel        string              `json:"system_level"`
	SystemStandard     string              `json:"system_standard"`
	Special            string              `json:"special"`
	TestMode           string              `json:"test_mode"`
	TeamLeadID         string              `json:"team_lead_id,omitempty"`
	ProjectManagerID   string              `json:"project_manager_id,omitempty"`
	EngineerIDs        []string            `json:"engineer_ids,omitempty"`
	EquipmentIDs       []string            `json:"equipment_ids,omitempty"`
	RequiredCodes      []string            `json:"required_codes,omitempty"`
	PlannedStart       string              `json:"planned_start,omitempty"`
	PlannedEnd         string              `json:"planned_end,omitempty"`
	ConflictStatus     string              `json:"conflict_status,omitempty"`
	TechReviewStatus   string              `json:"tech_review_status,omitempty"`
	TechReviewedAt     string              `json:"tech_reviewed_at,omitempty"`
	TechReviewedBy     string              `json:"tech_reviewed_by,omitempty"`
	TechReviewComment  string              `json:"tech_review_comment,omitempty"`
	ReportStatus       string              `json:"report_status,omitempty"`
	ReportUpdatedAt    string              `json:"report_updated_at,omitempty"`
	ReportUpdatedBy    string              `json:"report_updated_by,omitempty"`
	ReportRevision     uint64              `json:"report_revision"`
	ReportPreparedBy   string              `json:"report_prepared_by,omitempty"`
	ReportReviewedBy   string              `json:"report_reviewed_by,omitempty"`
	ReportIssuedBy     string              `json:"report_issued_by,omitempty"`
	Status             string              `json:"status"`
	ImplementationPlan *ImplementationPlan `json:"implementation_plan,omitempty"`

	// Version 是服务项状态版本：每次状态变更自增。读取方据此识别自己拿到的是不是最新一版，
	// 写入方可带上期望版本做条件更新（不匹配即 409 冲突），避免"后者静默覆盖前者"。
	Version uint64 `json:"version"`
}

// ReportRevision is an immutable report-version ledger projection. The row is retained after a
// correction; validity_status=VOID prevents new downloads while the old audit/download history stays intact.
type ReportRevision struct {
	ID                  uint64 `json:"id"`
	ServiceItemID       string `json:"service_item_id"`
	Revision            uint64 `json:"revision"`
	Status              string `json:"status"`
	ValidityStatus      string `json:"validity_status"`
	CorrectionRequestID string `json:"correction_request_id,omitempty"`
	CorrectionReason    string `json:"correction_reason,omitempty"`
	FileID              string `json:"file_id,omitempty"`
	FileName            string `json:"file_name,omitempty"`
	FileMIME            string `json:"file_mime,omitempty"`
	FileSize            uint64 `json:"file_size,omitempty"`
	FileSHA256          string `json:"file_sha256,omitempty"`
	PreparedBy          string `json:"prepared_by,omitempty"`
	PreparedAt          string `json:"prepared_at,omitempty"`
	ReviewedBy          string `json:"reviewed_by,omitempty"`
	ReviewedAt          string `json:"reviewed_at,omitempty"`
	IssuedBy            string `json:"issued_by,omitempty"`
	IssuedAt            string `json:"issued_at,omitempty"`
	ArchivedBy          string `json:"archived_by,omitempty"`
	ArchivedAt          string `json:"archived_at,omitempty"`
	InvalidatedBy       string `json:"invalidated_by,omitempty"`
	InvalidatedAt       string `json:"invalidated_at,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type Rule struct {
	TenantID  string `json:"-"`
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Trigger   string `json:"trigger"`
	Enabled   bool   `json:"enabled"`
	Updated   string `json:"updated"`
	Notes     string `json:"notes,omitempty"`
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
	TenantID         string `json:"tenant_id,omitempty"`
	ProjectCount     int    `json:"project_count"`
	InFlightProjects int    `json:"in_flight_projects"`
	RiskProjects     int    `json:"risk_projects"`
	// PendingProjectCreation is tenant-wide and shown only to roles allowed to
	// create projects. Available=false means the contract dependency could not
	// be queried; zero remains a real, distinguishable business result.
	PendingProjectCreation          int  `json:"pending_project_creation"`
	PendingProjectCreationAvailable bool `json:"pending_project_creation_available"`
	// UnknownStatusItems 是无法识别的服务项状态数量（数据异常）。
	// 这类状态会被保守地按最滞后处理，因此必须显式暴露，避免项目状态静默变化而无人发现。
	UnknownStatusItems int            `json:"unknown_status_items"`
	ServiceItems       int            `json:"service_items"`
	StatusCounts       map[string]int `json:"status_counts"`
}

// SlaOverdueItem 是超期/临近超期服务项的投影。Kind 区分两类口径：
// PLAN_END_OVERDUE 按计划完成时间判定；STATUS_DEADLINE_* 按 pm_sla 规则对
// 「停留在某状态的时长」判定（UpdatedAt 即进入当前状态的时间，每次状态推进都会刷新）。
type SlaOverdueItem struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	Site          string `json:"site"`
	Category      string `json:"category"`
	Status        string `json:"status"`
	PlannedEnd    string `json:"planned_end,omitempty"`
	OverdueHours  int64  `json:"overdue_hours"`
	Kind          string `json:"kind"`
	RuleName      string `json:"rule_name,omitempty"`
	RuleStatus    string `json:"rule_status,omitempty"`
	DeadlineHours int    `json:"deadline_hours,omitempty"`
	// UpdatedAt 供应用层按 SLA 规则计算停留时长，不对外输出。
	UpdatedAt time.Time `json:"-"`
	// StatusChangedAt 是服务项进入当前状态的时刻，是 SLA 停留时长的唯一基准。
	StatusChangedAt time.Time `json:"-"`
}

// SLA 口径常量。
const (
	SlaKindPlanEndOverdue    = "PLAN_END_OVERDUE"
	SlaKindStatusOverdue     = "STATUS_DEADLINE_OVERDUE"
	SlaKindStatusApproaching = "STATUS_DEADLINE_APPROACHING"
)

// Site 是站点主数据：项目/服务项的 site 原本是从合同复制的自由文本，
// 既无法按站点聚合，也没有坐标可供比对。站点台账给它一个稳定编码与坐标。
type Site struct {
	ID        string  `json:"id"`
	TenantID  string  `json:"-"`
	SiteCode  string  `json:"site_code"`
	Name      string  `json:"name"`
	Address   string  `json:"address,omitempty"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	// HasCoordinates 区分"坐标为 0"与"尚未采集"：0,0 是合法坐标（几内亚湾），
	// 因此不能拿零值当缺失判断。
	HasCoordinates bool   `json:"has_coordinates"`
	Status         string `json:"status"`
	Notes          string `json:"notes,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// ValidateSite 校验站点档案：编码与名称必填，坐标必须在合法经纬度范围内。
// 坐标可选（尚未采集），但一旦给出就必须合法——错误的坐标比没有坐标更危险。
func ValidateSite(item Site) error {
	if strings.TrimSpace(item.SiteCode) == "" || strings.TrimSpace(item.Name) == "" {
		return errors.New("site code and name are required")
	}
	if item.Status != "" && item.Status != "ACTIVE" && item.Status != "DISABLED" {
		return errors.New("site status must be ACTIVE or DISABLED")
	}
	if item.HasCoordinates || item.Latitude != 0 || item.Longitude != 0 {
		if item.Latitude < -90 || item.Latitude > 90 {
			return errors.New("site latitude must be between -90 and 90")
		}
		if item.Longitude < -180 || item.Longitude > 180 {
			return errors.New("site longitude must be between -180 and 180")
		}
	}
	return nil
}

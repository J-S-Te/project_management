package domain

import "time"

// ContractActivation is the idempotent contract-to-project handoff owned by the contract system.
type ContractActivation struct {
	ContractID              string            `json:"contract_id"`
	ContractVersion         string            `json:"contract_version"`
	ContractName            string            `json:"contract_name"`
	Customer                string            `json:"customer"`
	// CustomerID 是合同系统持有的客户标识；旧版调用方未携带时保持空，项目仍以名称记录客户。
	CustomerID              string            `json:"customer_id,omitempty"`
	EffectiveAt             time.Time         `json:"effective_at"`
	StampedContractUploaded bool              `json:"stamped_contract_uploaded"`
	Services                []ContractService `json:"services"`
}

type ContractService struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	Site        string `json:"site"`
	Batch       string `json:"batch"`
	Category    string `json:"category"`
	System      string `json:"system"`
	SystemLevel string `json:"system_level"`
	Requirement string `json:"requirement"`
	TestMode    string `json:"test_mode"`
}

type DeliveryEvent struct {
	ID            string         `json:"id"`
	TenantID      string         `json:"-"`
	ProjectID     string         `json:"project_id"`
	ServiceItemID string         `json:"service_item_id,omitempty"`
	Type          string         `json:"type"`
	ActorUserID   string         `json:"actor_user_id"`
	Payload       map[string]any `json:"payload"`
	CreatedAt     time.Time      `json:"created_at"`
}

type Capability struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"-"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	ResourceName string    `json:"resource_name"`
	// UserID 是人员能力档案关联的平台 user_id：实施计划的人员行既可按档案编号、
	// 也可按平台 user_id 提交，服务端统一解析到档案行，消除人员标识双轨制。
	// 仅 PERSON 类型有意义；设备行保持空。
	UserID        string    `json:"user_id,omitempty"`
	Codes         []string  `json:"codes"`
	ValidFrom     time.Time `json:"valid_from"`
	ValidUntil    time.Time `json:"valid_until"`
	Status        string    `json:"status"`
	// UsageScope 为 ANY（可借出）或 COMPANY_ONLY（仅在公司使用，不可借出）。
	UsageScope string `json:"usage_scope"`
	// Presence 是派生状态：IN_COMPANY / OUT_OF_COMPANY（借出中）。仅设备有意义。
	Presence string `json:"presence,omitempty"`
	// BorrowedBy 在借出中时说明占用方（项目号）；BorrowedServiceItemID 供设备维护页发起归还。
	BorrowedBy            string `json:"borrowed_by,omitempty"`
	BorrowedServiceItemID string `json:"borrowed_service_item_id,omitempty"`
	BorrowedWindow        string `json:"borrowed_window,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// 设备使用范围：可借出 / 仅在公司使用。
const (
	EquipmentUsageAny         = "ANY"
	EquipmentUsageCompanyOnly = "COMPANY_ONLY"
)

// 设备在位状态：由占用时段派生，不落库。
const (
	EquipmentPresenceInCompany    = "IN_COMPANY"
	EquipmentPresenceOutOfCompany = "OUT_OF_COMPANY"
)

type DecompositionAdjustmentInput struct {
	Reason               string            `json:"reason"`
	SupplementContractID string            `json:"supplement_contract_id"`
	Items                []ContractService `json:"items"`
}

type TeamAssignmentInput struct {
	TeamLeadID string `json:"team_lead_id"`
}

// ExecutionAssignmentInput 是团队负责人指派项目经理与实施工程师的输入。
// 设备清单已在「实施准备」阶段登记，不再随指派提交。
type ExecutionAssignmentInput struct {
	ProjectManagerID string   `json:"project_manager_id"`
	EngineerIDs      []string `json:"engineer_ids"`
	RequiredCodes    []string `json:"required_codes"`
}

// PlanResourceInput 是实施计划提交时的人员/设备行：只接受资源标识与使用时段，
// 名称、资质与有效期由服务端从能力档案解析，避免客户端伪造派工快照。
type PlanResourceInput struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	WindowStart  string `json:"window_start,omitempty"`
	WindowEnd    string `json:"window_end,omitempty"`
	Note         string `json:"note,omitempty"`
}

// PlanResource 是实施计划里的人员/设备清单行，资质与有效期在发布计划时快照，
// 让已发布的计划保留当时的派工依据。
type PlanResource struct {
	ResourceType string   `json:"resource_type"`
	ResourceID   string   `json:"resource_id"`
	ResourceName string   `json:"resource_name"`
	Codes        []string `json:"codes"`
	ValidUntil   string   `json:"valid_until,omitempty"`
	WindowStart  string   `json:"window_start,omitempty"`
	WindowEnd    string   `json:"window_end,omitempty"`
	Note         string   `json:"note,omitempty"`
	// ReturnedAt 为归还时间；归还后的设备行保留历史，但不再占用设备、也不再算"不在公司"。
	ReturnedAt string `json:"returned_at,omitempty"`
}

type ImplementationPlanInput struct {
	PlannedStart        string `json:"planned_start"`
	PlannedEnd          string `json:"planned_end"`
	SitePlan            string `json:"site_plan"`
	PenetrationTestPlan string `json:"penetration_test_plan"`
	AuthDocNo           string `json:"auth_doc_no"`
	AuthStart           string `json:"auth_start"`
	AuthEnd             string `json:"auth_end"`
	AuthScope           string `json:"auth_scope"`
	TestScope           string `json:"test_scope"`
	TestWindow          string `json:"test_window"`
	EmergencyContact    string `json:"emergency_contact"`
	RollbackPlan        string `json:"rollback_plan"`
	// Personnel 是现场实施的人员清单；至少一名人员才能发布计划。
	// 设备清单在「实施准备」阶段提交，计划阶段不再选择设备。
	Personnel []PlanResourceInput `json:"personnel"`
}

// ImplementationPlan 是与服务项绑定的实施计划读写模型，包含渗透测试专项合规要素。
type ImplementationPlan struct {
	PlannedStart        string         `json:"planned_start,omitempty"`
	PlannedEnd          string         `json:"planned_end,omitempty"`
	SitePlan            string         `json:"site_plan,omitempty"`
	PenetrationTestPlan string         `json:"penetration_test_plan,omitempty"`
	AuthDocNo           string         `json:"auth_doc_no,omitempty"`
	AuthStart           string         `json:"auth_start,omitempty"`
	AuthEnd             string         `json:"auth_end,omitempty"`
	AuthScope           string         `json:"auth_scope,omitempty"`
	TestScope           string         `json:"test_scope,omitempty"`
	TestWindow          string         `json:"test_window,omitempty"`
	EmergencyContact    string         `json:"emergency_contact,omitempty"`
	RollbackPlan        string         `json:"rollback_plan,omitempty"`
	Personnel           []PlanResource `json:"personnel"`
	Equipment           []PlanResource `json:"equipment"`
}

// EquipmentReservation 是一条设备占用记录，用于「实施准备」阶段判断设备在某段时间内
// 是否已被其他服务项占用，并向界面解释占用方。
type EquipmentReservation struct {
	ServiceItemID string `json:"service_item_id"`
	ProjectID     string `json:"project_id"`
	Customer      string `json:"customer,omitempty"`
	ResourceID    string `json:"resource_id"`
	ResourceName  string `json:"resource_name"`
	WindowStart   string `json:"window_start"`
	WindowEnd     string `json:"window_end"`
}

type SpecialMethodReviewInput struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

type ReportStatusInput struct {
	Phase string `json:"phase"`
}

// PreparationInput 是实施准备提交的内容。设备申领不再是自由文本：设备清单
// （Equipment）本身就说明申领了哪些设备、在什么时段使用，因此不再单列申领单号。
type PreparationInput struct {
	TravelRequestID string `json:"travel_request_id"`
	Notes           string `json:"notes"`
	// Equipment 是实施准备确定的设备清单；每行带使用时段，服务端按占用区间硬拦重叠。
	Equipment []PlanResourceInput `json:"equipment"`
}

type FieldRecordInput struct {
	RawData      string   `json:"raw_data"`
	Environment  string   `json:"environment"`
	EvidenceURLs []string `json:"evidence_urls"`
}

type DeviationInput struct {
	Description string `json:"description"`
	Severity    string `json:"severity"`
	EvidenceURL string `json:"evidence_url"`
}

type DeviationReviewInput struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

type ConflictCheckResult struct {
	Passed    bool     `json:"passed"`
	Conflicts []string `json:"conflicts"`
}

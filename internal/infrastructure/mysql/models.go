package mysql

import "time"

type projectRecord struct {
	ID                string `gorm:"primaryKey;size:32"`
	TenantID          string `gorm:"size:64;not null;index:idx_pm_project_tenant_status,priority:1"`
	OwnerOrgID        string `gorm:"size:64;not null;index:idx_pm_project_tenant_owner_org,priority:2"`
	Name              string `gorm:"size:255;not null"`
	Customer          string `gorm:"size:255;not null"`
	Contract          string `gorm:"size:64;not null"`
	ContractVersion   string `gorm:"size:64;not null"`
	SupplementStatus  string `gorm:"size:32;not null"`
	Services          int
	Category          string `gorm:"size:255"`
	Team              string `gorm:"size:128"`
	Manager           string `gorm:"size:128"`
	OwnerIdentityID   string `gorm:"size:128;not null;index:idx_pm_project_tenant_owner_identity,priority:2"`
	ManagerIdentityID string `gorm:"size:128;not null;index:idx_pm_project_tenant_manager_identity,priority:2"`
	Health            string `gorm:"size:32"`
	Status            string `gorm:"size:32;not null;index:idx_pm_project_tenant_status,priority:2"`
	Progress          int
	Due               string `gorm:"size:64"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (projectRecord) TableName() string { return "pm_project" }

type serviceItemRecord struct {
	ID               string `gorm:"primaryKey;size:32"`
	TenantID         string `gorm:"size:64;not null;index:idx_pm_service_tenant_project,priority:1"`
	ProjectID        string `gorm:"size:32;not null;index:idx_pm_service_tenant_project,priority:2"`
	SourceServiceID  string `gorm:"type:text;not null"`
	Batch            string `gorm:"size:64"`
	Site             string `gorm:"size:255"`
	Category         string `gorm:"size:255"`
	Requirement      string `gorm:"type:text"`
	System           string `gorm:"size:128"`
	SystemLevel      string `gorm:"size:64"`
	Special          string `gorm:"size:16"`
	TestMode         string `gorm:"size:32;not null"`
	TeamLeadID       string `gorm:"size:64"`
	ProjectManagerID string `gorm:"size:64"`
	EngineerIDs      []byte `gorm:"type:json"`
	EquipmentIDs     []byte `gorm:"type:json"`
	RequiredCodes    []byte `gorm:"type:json"`
	PlannedStart      *time.Time
	PlannedEnd        *time.Time
	ConflictStatus    string    `gorm:"size:32;not null"`
	TechReviewStatus  string    `gorm:"size:32;not null"`
	TechReviewedAt    *time.Time
	TechReviewedBy    string    `gorm:"size:64;not null"`
	TechReviewComment string    `gorm:"size:512;not null"`
	ReportStatus      string    `gorm:"size:32;not null"`
	ReportUpdatedAt   *time.Time
	ReportUpdatedBy   string    `gorm:"size:64;not null"`
	Status            string    `gorm:"size:32;not null"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	UpdatedBy         string `gorm:"size:64"`
}

func (serviceItemRecord) TableName() string { return "pm_service_item" }

// implPlanRecord 保存实施计划与渗透测试专项合规要素（授权书、白名单范围、测试时间窗、
// 应急联系人、回滚方案等），与服务项 1:1 关联，由 EventImplementationPlanned 幂等写入。
type implPlanRecord struct {
	ID                string `gorm:"primaryKey;size:32"`
	TenantID          string `gorm:"size:64;not null"`
	ServiceItemID     string `gorm:"size:32;not null;index:idx_pm_impl_plan_item,priority:2"`
	PlannedStart      *time.Time
	PlannedEnd        *time.Time
	SitePlan          string `gorm:"type:text;not null"`
	PenetrationTestPlan string `gorm:"type:text;not null"`
	AuthDocNo         string `gorm:"size:128;not null"`
	AuthStart         *time.Time
	AuthEnd           *time.Time
	AuthScope         string `gorm:"type:text;not null"`
	TestScope         string `gorm:"type:text;not null"`
	TestWindow        string `gorm:"size:128;not null"`
	EmergencyContact  string `gorm:"size:128;not null"`
	RollbackPlan      string `gorm:"type:text;not null"`
	UpdatedAt         time.Time
	UpdatedBy         string `gorm:"size:64;not null"`
}

func (implPlanRecord) TableName() string { return "pm_impl_plan" }

// ruleRow 是五套配置表的统一读取视图。各表只拥有自己需要的列，读取时用同一条
// SELECT 扫描进该结构，未出现在目标表中的列保持零值。
type ruleRow struct {
	ID            int64
	TenantID      string
	Kind          string
	Name          string
	Scope         string
	CheckType     string
	Threshold     string
	Trigger       string
	Target        string
	RoleCode      string
	FieldName     string
	AccessLevel   string
	Status        string
	DeadlineHours int
	RemindHours   int
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	UpdatedBy     string
}

type splitRuleRecord struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	TenantID  string `gorm:"size:64;not null;index:idx_pm_split_rule_tenant_kind,priority:1"`
	Kind      string `gorm:"size:64;not null;index:idx_pm_split_rule_tenant_kind,priority:2"`
	Name      string `gorm:"size:255;not null"`
	Scope     string `gorm:"size:255;not null"`
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy string `gorm:"size:64"`
}

func (splitRuleRecord) TableName() string { return "pm_split_rule" }

type warningRuleRecord struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	TenantID  string `gorm:"size:64;not null;index:idx_pm_warning_rule_tenant_kind,priority:1"`
	Kind      string `gorm:"size:64;not null;index:idx_pm_warning_rule_tenant_kind,priority:2"`
	Name      string `gorm:"size:255;not null"`
	CheckType string `gorm:"size:64;not null"`
	Threshold string `gorm:"size:128;not null"`
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy string `gorm:"size:64"`
}

func (warningRuleRecord) TableName() string { return "pm_warning_rule" }

type automationRecord struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	TenantID  string `gorm:"size:64;not null;index:idx_pm_automation_tenant_kind,priority:1"`
	Kind      string `gorm:"size:64;not null;index:idx_pm_automation_tenant_kind,priority:2"`
	Name      string `gorm:"size:255;not null"`
	Trigger   string `gorm:"size:128;not null"`
	Target    string `gorm:"size:255;not null"`
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy string `gorm:"size:64"`
}

func (automationRecord) TableName() string { return "pm_automation" }

type fieldPermissionRecord struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	TenantID    string `gorm:"size:64;not null;index:idx_pm_field_permission_tenant_kind,priority:1"`
	Kind        string `gorm:"size:64;not null;index:idx_pm_field_permission_tenant_kind,priority:2"`
	Name        string `gorm:"size:255;not null"`
	RoleCode    string `gorm:"size:64;not null"`
	FieldName   string `gorm:"size:128;not null"`
	AccessLevel string `gorm:"size:32;not null"`
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	UpdatedBy   string `gorm:"size:64"`
}

func (fieldPermissionRecord) TableName() string { return "pm_field_permission" }

type slaRecord struct {
	ID            int64  `gorm:"primaryKey;autoIncrement"`
	TenantID      string `gorm:"size:64;not null;index:idx_pm_sla_tenant_kind,priority:1"`
	Kind          string `gorm:"size:64;not null;index:idx_pm_sla_tenant_kind,priority:2"`
	Name          string `gorm:"size:255;not null"`
	Status        string `gorm:"size:64;not null"`
	DeadlineHours int
	RemindHours   int
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	UpdatedBy     string `gorm:"size:64"`
}

func (slaRecord) TableName() string { return "pm_sla" }

type ruleRecord struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	TenantID  string `gorm:"size:64;not null;index:idx_pm_rule_tenant_kind,priority:1"`
	Kind      string `gorm:"size:64;not null;index:idx_pm_rule_tenant_kind,priority:2"`
	Name      string `gorm:"size:255;not null"`
	Scope     string `gorm:"size:255;not null"`
	Trigger   string `gorm:"type:text"`
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy string `gorm:"size:64"`
}

func (ruleRecord) TableName() string { return "pm_rule" }

type deliveryEventRecord struct {
	ID            string    `gorm:"primaryKey;size:32"`
	TenantID      string    `gorm:"size:64;not null;index:idx_pm_event_tenant_project_time,priority:1"`
	ProjectID     string    `gorm:"size:32;not null;index:idx_pm_event_tenant_project_time,priority:2"`
	ServiceItemID string    `gorm:"size:32;not null"`
	EventType     string    `gorm:"size:64;not null"`
	ActorUserID   string    `gorm:"size:64;not null"`
	Payload       []byte    `gorm:"type:json;not null"`
	CreatedAt     time.Time `gorm:"index:idx_pm_event_tenant_project_time,priority:3"`
}

func (deliveryEventRecord) TableName() string { return "pm_delivery_event" }

type capabilityRecord struct {
	ID              string `gorm:"primaryKey;size:32"`
	TenantID        string `gorm:"size:64;not null"`
	ResourceType    string `gorm:"size:16;not null"`
	ResourceID      string `gorm:"size:64;not null"`
	ResourceName    string `gorm:"size:128;not null"`
	CapabilityCodes []byte `gorm:"type:json;not null"`
	ValidFrom       *time.Time
	ValidUntil      *time.Time
	Status          string `gorm:"size:16;not null"`
	UpdatedAt       time.Time
	UpdatedBy       string `gorm:"size:64;not null"`
}

func (capabilityRecord) TableName() string { return "pm_capability" }

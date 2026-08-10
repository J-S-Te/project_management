package mysql

import "time"

type projectRecord struct {
	ID               string `gorm:"primaryKey;size:32"`
	TenantID         string `gorm:"size:64;not null;index:idx_pm_project_tenant_status,priority:1"`
	Name             string `gorm:"size:255;not null"`
	Customer         string `gorm:"size:255;not null"`
	Contract         string `gorm:"size:64;not null"`
	ContractVersion  string `gorm:"size:64;not null"`
	SupplementStatus string `gorm:"size:32;not null"`
	Services         int
	Category         string `gorm:"size:255"`
	Team             string `gorm:"size:128"`
	Manager          string `gorm:"size:128"`
	Health           string `gorm:"size:32"`
	Status           string `gorm:"size:32;not null;index:idx_pm_project_tenant_status,priority:2"`
	Progress         int
	Due              string `gorm:"size:64"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
	Special          string `gorm:"size:16"`
	TestMode         string `gorm:"size:32;not null"`
	TeamLeadID       string `gorm:"size:64"`
	ProjectManagerID string `gorm:"size:64"`
	EngineerIDs      []byte `gorm:"type:json"`
	EquipmentIDs     []byte `gorm:"type:json"`
	RequiredCodes    []byte `gorm:"type:json"`
	PlannedStart     *time.Time
	PlannedEnd       *time.Time
	ConflictStatus   string `gorm:"size:32;not null"`
	Status           string `gorm:"size:32;not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	UpdatedBy        string `gorm:"size:64"`
}

func (serviceItemRecord) TableName() string { return "pm_service_item" }

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

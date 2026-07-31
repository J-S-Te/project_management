package mysql

import "time"

type projectRecord struct {
	ID        string `gorm:"primaryKey;size:32"`
	TenantID  string `gorm:"size:64;not null;index:idx_pm_project_tenant_status,priority:1"`
	Name      string `gorm:"size:255;not null"`
	Customer  string `gorm:"size:255;not null"`
	Contract  string `gorm:"size:64;not null"`
	Services  int
	Category  string `gorm:"size:255"`
	Team      string `gorm:"size:128"`
	Manager   string `gorm:"size:128"`
	Health    string `gorm:"size:32"`
	Status    string `gorm:"size:32;not null;index:idx_pm_project_tenant_status,priority:2"`
	Progress  int
	Due       string `gorm:"size:64"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (projectRecord) TableName() string { return "pm_project" }

type serviceItemRecord struct {
	ID          string `gorm:"primaryKey;size:32"`
	TenantID    string `gorm:"size:64;not null;index:idx_pm_service_tenant_project,priority:1"`
	ProjectID   string `gorm:"size:32;not null;index:idx_pm_service_tenant_project,priority:2"`
	Batch       string `gorm:"size:64"`
	Site        string `gorm:"size:255"`
	Category    string `gorm:"size:255"`
	Requirement string `gorm:"type:text"`
	System      string `gorm:"size:128"`
	Special     string `gorm:"size:16"`
	Status      string `gorm:"size:32;not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	UpdatedBy   string `gorm:"size:64"`
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

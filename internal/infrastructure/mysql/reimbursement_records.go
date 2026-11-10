package mysql

import "time"

type expenseParticipantRow struct {
	TenantID   string `gorm:"primaryKey;size:128"`
	ProjectID  string `gorm:"primaryKey;size:128"`
	SubjectID  string `gorm:"primaryKey;size:128"`
	Status     string `gorm:"size:16"`
	ValidFrom  time.Time
	ValidUntil time.Time
	GrantedBy  string `gorm:"size:128"`
	Reason     string `gorm:"size:1000"`
	Version    uint64
}
type expenseCursorRow struct {
	TenantID           string `gorm:"primaryKey;size:128"`
	SourceEnvironment  string `gorm:"primaryKey;size:64"`
	ClaimID            string `gorm:"primaryKey;size:128"`
	ProjectionRevision uint64
	SnapshotHash       string `gorm:"size:64"`
	UpdatedAt          time.Time
}
type expenseInboxRow struct {
	TenantID          string `gorm:"primaryKey;size:128"`
	SourceEnvironment string `gorm:"primaryKey;size:64"`
	EventID           string `gorm:"primaryKey;size:128"`
	ClaimID           string `gorm:"size:128"`
	EventHash         string `gorm:"size:64"`
	Receipt           string `gorm:"type:longtext"`
	ReceivedAt        time.Time
}
type expenseSnapshotRow struct {
	TenantID           string `gorm:"primaryKey;size:128"`
	SourceEnvironment  string `gorm:"primaryKey;size:64"`
	ClaimID            string `gorm:"primaryKey;size:128"`
	ProjectionRevision uint64 `gorm:"primaryKey"`
	SnapshotHash       string `gorm:"size:64"`
	Payload            string `gorm:"type:longtext"`
}
type expenseEffectRow struct {
	TenantID          string `gorm:"primaryKey;size:128;uniqueIndex:expense_effect_business,priority:1"`
	SourceEnvironment string `gorm:"primaryKey;size:64;uniqueIndex:expense_effect_business,priority:2"`
	EffectID          string `gorm:"primaryKey;size:128"`
	ClaimID           string `gorm:"size:128;index"`
	PaymentRecordID   string `gorm:"size:128;uniqueIndex:expense_effect_business,priority:3"`
	Kind              string `gorm:"size:32;uniqueIndex:expense_effect_business,priority:4"`
	EffectHash        string `gorm:"size:64"`
	Payload           string `gorm:"type:longtext"`
}
type expenseAllocationRow struct {
	TenantID          string `gorm:"primaryKey;size:128"`
	SourceEnvironment string `gorm:"primaryKey;size:64"`
	EffectID          string `gorm:"primaryKey;size:128"`
	ProjectID         string `gorm:"primaryKey;size:128"`
	ServiceItemID     string `gorm:"primaryKey;size:128"`
	FeeType           string `gorm:"primaryKey;size:64"`
	AmountMinor       int64
}
type expenseProjectionRow struct {
	TenantID          string `gorm:"primaryKey;size:128"`
	SourceEnvironment string `gorm:"primaryKey;size:64"`
	ClaimID           string `gorm:"primaryKey;size:128"`
	ProjectID         string `gorm:"primaryKey;size:128"`
	ServiceItemID     string `gorm:"primaryKey;size:128"`
	FeeType           string `gorm:"primaryKey;size:64"`
	PaidNetMinor      int64
}
type expenseRepairRow struct {
	ID                string `gorm:"primaryKey;size:128"`
	TenantID          string `gorm:"size:128"`
	SourceEnvironment string `gorm:"size:64"`
	ClaimID           string `gorm:"size:128"`
	Actor             string `gorm:"size:128"`
	Reason            string `gorm:"size:1000"`
	CreatedAt         time.Time
}
type expenseConflictRow struct {
	ID                string `gorm:"primaryKey;size:128"`
	TenantID          string `gorm:"size:128"`
	SourceEnvironment string `gorm:"size:64"`
	ClaimID           string `gorm:"size:128"`
	EventID           string `gorm:"size:128"`
	EventHash         string `gorm:"size:64"`
	CreatedAt         time.Time
}

var expenseTables = []struct {
	name  string
	model any
}{
	{"pm_expense_participant", &expenseParticipantRow{}},
	{"pm_expense_cursor", &expenseCursorRow{}},
	{"pm_expense_inbox", &expenseInboxRow{}},
	{"pm_expense_snapshot", &expenseSnapshotRow{}},
	{"pm_expense_effect", &expenseEffectRow{}},
	{"pm_expense_allocation", &expenseAllocationRow{}},
	{"pm_expense_projection", &expenseProjectionRow{}},
	{"pm_expense_repair", &expenseRepairRow{}},
	{"pm_expense_conflict", &expenseConflictRow{}},
}

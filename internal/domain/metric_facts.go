package domain

import (
	"errors"
	"strings"
	"time"
)

// MetricInterval 是UTC存储、按北京业务日解释的半开时段。
type MetricInterval struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ValidateMetricIntervals 校验明确容量或实际时段，禁止跨日、重叠和非整分钟。
// 参数：day 为北京业务日，intervals 为已按开始时刻排序的半开时段。
// 返回值：总分钟；非法日期、越界或重复时段返回错误。
func ValidateMetricIntervals(day string, intervals []MetricInterval) (int64, error) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	start, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil || len(intervals) > 24 {
		return 0, errors.New("容量日期或时段数量无效")
	}
	end := start.AddDate(0, 0, 1)
	var minutes int64
	previous := start
	for _, interval := range intervals {
		seconds := interval.End.Sub(interval.Start)
		if interval.Start.Before(previous) || interval.Start.Before(start) || interval.End.After(end) || seconds <= 0 || seconds%time.Minute != 0 || interval.Start.Second() != 0 || interval.Start.Nanosecond() != 0 {
			return 0, errors.New("时段须在同一北京业务日内，精确到分钟且不能重叠")
		}
		minutes += int64(seconds / time.Minute)
		previous = interval.End
	}
	return minutes, nil
}

// MetricContains 检查实际时段是否完整落在明确容量内。
// 参数：capacity 为容量半开时段，actual 为实际半开时段。
// 返回值：完整覆盖时为true，否则为false。
func MetricContains(capacity []MetricInterval, actual MetricInterval) bool {
	cursor := actual.Start
	for _, interval := range capacity {
		if interval.End.Before(cursor) || interval.End.Equal(cursor) {
			continue
		}
		if interval.Start.After(cursor) {
			return false
		}
		cursor = interval.End
		if !cursor.Before(actual.End) {
			return true
		}
	}
	return false
}

// MetricReviewReason 判断审核退回原因是否属于受控词表。
// 参数：code 为原因码，note 为补充说明。
// 返回值：受控原因且说明完整时为true。
func MetricReviewReason(code, note string) bool {
	switch code {
	case "EVIDENCE_INCOMPLETE", "TECHNICAL_ERROR", "FORMAT_ERROR", "SCOPE_MISMATCH", "OTHER":
		return strings.TrimSpace(note) != "" && len([]rune(note)) <= 1000
	}
	return false
}

type ReportReviewInput struct {
	SubjectType     string `json:"subject_type"`
	Revision        uint64 `json:"revision"`
	ExpectedVersion uint64 `json:"expected_version"`
	RoundID         string `json:"round_id"`
	Decision        string `json:"decision"`
	ReasonCode      string `json:"reason_code"`
	Note            string `json:"note"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type ReportReviewRound struct {
	FileID         string     `json:"file_id" gorm:"size:64"`
	FileSHA256     string     `json:"file_sha256" gorm:"size:64"`
	ID             string     `json:"id" gorm:"primaryKey;size:32"`
	TenantID       string     `json:"-" gorm:"size:64;not null;index"`
	ProjectID      string     `json:"project_id" gorm:"size:32;not null"`
	ServiceItemID  string     `json:"service_item_id" gorm:"size:32;not null"`
	SubjectType    string     `json:"subject_type" gorm:"size:32;not null"`
	SubjectID      string     `json:"subject_id" gorm:"size:32;not null"`
	Revision       uint64     `json:"revision"`
	RoundNo        uint64     `json:"round_no"`
	SourceVersion  uint64     `json:"source_version"`
	OrganizationID string     `json:"organization_id" gorm:"size:64"`
	IdentityID     string     `json:"identity_id" gorm:"size:64"`
	PreparedBy     string     `json:"prepared_by" gorm:"size:64"`
	SubmittedBy    string     `json:"submitted_by" gorm:"size:64"`
	SubmittedAt    time.Time  `json:"submitted_at"`
	Decision       string     `json:"decision" gorm:"size:16"`
	DecidedBy      string     `json:"decided_by" gorm:"size:64"`
	DecidedAt      *time.Time `json:"decided_at"`
	ReasonCode     string     `json:"reason_code" gorm:"size:32"`
	Note           string     `json:"note" gorm:"size:1000"`
}

type ResourceCapacityInput struct {
	ResourceType          string           `json:"resource_type"`
	ResourceID            string           `json:"resource_id"`
	BusinessDate          string           `json:"business_date"`
	OrganizationID        string           `json:"organization_id"`
	OrganizationWitnessID string           `json:"organization_witness_id"`
	Intervals             []MetricInterval `json:"intervals"`
	Note                  string           `json:"note"`
	ExpectedVersion       uint64           `json:"expected_version"`
	IdempotencyKey        string           `json:"idempotency_key"`
}

type ResourceCapacity struct {
	ID                    string           `json:"id" gorm:"primaryKey;size:32"`
	TenantID              string           `json:"-" gorm:"size:64;index"`
	ResourceType          string           `json:"resource_type" gorm:"size:16"`
	ResourceID            string           `json:"resource_id" gorm:"size:64"`
	IdentityID            string           `json:"identity_id" gorm:"size:64"`
	BusinessDate          string           `json:"business_date" gorm:"size:10"`
	OrganizationID        string           `json:"organization_id" gorm:"size:64"`
	OrganizationWitnessID string           `json:"organization_witness_id" gorm:"size:64"`
	Intervals             []MetricInterval `json:"intervals" gorm:"serializer:json;type:json"`
	Minutes               int64            `json:"minutes"`
	Version               uint64           `json:"version"`
	Status                string           `json:"status" gorm:"size:16"`
	Note                  string           `json:"note" gorm:"size:1000"`
	PublishedBy           string           `json:"published_by" gorm:"size:64"`
	CreatedAt             time.Time        `json:"created_at"`
}

type ResourceUsageInput struct {
	ResourceType   string    `json:"resource_type"`
	ResourceID     string    `json:"resource_id"`
	ServiceItemID  string    `json:"service_item_id"`
	BusinessDate   string    `json:"business_date"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	Note           string    `json:"note"`
	Evidence       string    `json:"evidence"`
	CorrectsID     string    `json:"corrects_id"`
	IdempotencyKey string    `json:"idempotency_key"`
}

type ResourceUsageDecision struct {
	ExpectedVersion uint64 `json:"expected_version"`
	Decision        string `json:"decision"`
	Note            string `json:"note"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type ResourceUsage struct {
	ID             string    `json:"id" gorm:"primaryKey;size:32"`
	TenantID       string    `json:"-" gorm:"size:64;index"`
	ResourceType   string    `json:"resource_type" gorm:"size:16"`
	ResourceID     string    `json:"resource_id" gorm:"size:64"`
	IdentityID     string    `json:"identity_id" gorm:"size:64"`
	ProjectID      string    `json:"project_id" gorm:"size:32"`
	ServiceItemID  string    `json:"service_item_id" gorm:"size:32"`
	BusinessDate   string    `json:"business_date" gorm:"size:10"`
	OrganizationID string    `json:"organization_id" gorm:"size:64"`
	CapacityID     string    `json:"capacity_id" gorm:"size:32"`
	Start          time.Time `json:"start" gorm:"column:started_at"`
	End            time.Time `json:"end" gorm:"column:ended_at"`
	Minutes        int64     `json:"minutes"`
	Status         string    `json:"status" gorm:"size:16"`
	Version        uint64    `json:"version"`
	Note           string    `json:"note" gorm:"size:1000"`
	Evidence       string    `json:"evidence" gorm:"size:1000"`
	CorrectsID     string    `json:"corrects_id" gorm:"size:32"`
	CreatedBy      string    `json:"created_by" gorm:"size:64"`
	SubmittedBy    string    `json:"submitted_by" gorm:"size:64"`
	DecidedBy      string    `json:"decided_by" gorm:"size:64"`
	DecisionNote   string    `json:"decision_note" gorm:"size:1000"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type MetricFactRow struct {
	SubjectID       string `json:"subject_id,omitempty"`
	Kind            string `json:"kind"`
	RecordID        string `json:"record_id"`
	OrganizationID  string `json:"organization_id"`
	IdentityID      string `json:"identity_id"`
	ProjectID       string `json:"project_id"`
	Period          string `json:"period"`
	Status          string `json:"status"`
	FirstDecidedAt  string `json:"first_decided_at,omitempty"`
	ReasonCode      string `json:"reason_code,omitempty"`
	ActualMinutes   int64  `json:"actual_minutes"`
	CapacityMinutes int64  `json:"capacity_minutes"`
	PendingCount    int64  `json:"pending_count"`
	UnknownCount    int64  `json:"unknown_count"`
}

type MetricFactsSnapshot struct {
	SchemaVersion       int             `json:"schema_version"`
	TenantID            string          `json:"tenant_id"`
	SourceCode          string          `json:"source_code"`
	SnapshotAt          string          `json:"snapshot_at"`
	SourceUpdatedAt     string          `json:"source_updated_at"`
	CollectionStartedAt string          `json:"collection_started_at"`
	Complete            bool            `json:"complete"`
	Basis               string          `json:"basis"`
	Rows                []MetricFactRow `json:"rows"`
}

package domain

import "time"

// MetricCalendarRule 是组织某个工作日（1=周一，7=周日）的明确可用时段。
// 没有规则的组织不推算容量；规则按组织+星期版本化保存。
type MetricCalendarRule struct {
	ID                    string           `json:"id"`
	TenantID              string           `json:"-"`
	OrganizationID        string           `json:"organization_id"`
	Weekday               int              `json:"weekday"`
	Intervals             []MetricInterval `json:"intervals" gorm:"serializer:json;type:json"`
	Version               uint64           `json:"version"`
	Note                  string           `json:"note"`
	OrganizationWitnessID string           `json:"organization_witness_id"`
	UpdatedBy             string           `json:"updated_by"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

// MetricCalendarRuleInput 是一次组织工作日规则的版本化保存命令。
type MetricCalendarRuleInput struct {
	OrganizationID        string           `json:"organization_id"`
	Weekday               int              `json:"weekday"`
	Intervals             []MetricInterval `json:"intervals"`
	ExpectedVersion       uint64           `json:"expected_version"`
	Note                  string           `json:"note"`
	OrganizationWitnessID string           `json:"organization_witness_id"`
	IdempotencyKey        string           `json:"idempotency_key"`
}

// MetricCalendarException 是资源/组织在某天的容量例外。
// kind 为 LEAVE、OVERTIME、DOWNTIME 或 HOLIDAY；intervals 为该天最终容量，
// 不是与组织日历的增量，避免生成器重复推断。
type MetricCalendarException struct {
	ID             string           `json:"id"`
	TenantID       string           `json:"-"`
	OrganizationID string           `json:"organization_id"`
	ResourceType   string           `json:"resource_type"`
	ResourceID     string           `json:"resource_id"`
	IdentityID     string           `json:"identity_id,omitempty"`
	BusinessDate   string           `json:"business_date"`
	Kind           string           `json:"kind"`
	Intervals      []MetricInterval `json:"intervals" gorm:"serializer:json;type:json"`
	Version        uint64           `json:"version"`
	Note           string           `json:"note"`
	CreatedBy      string           `json:"created_by"`
	CreatedAt      time.Time        `json:"created_at"`
}

// MetricCalendarExceptionInput 是资源例外的版本化保存命令。
type MetricCalendarExceptionInput struct {
	OrganizationID  string           `json:"organization_id"`
	ResourceType    string           `json:"resource_type"`
	ResourceID      string           `json:"resource_id"`
	BusinessDate    string           `json:"business_date"`
	Kind            string           `json:"kind"`
	Intervals       []MetricInterval `json:"intervals"`
	ExpectedVersion uint64           `json:"expected_version"`
	Note            string           `json:"note"`
	IdempotencyKey  string           `json:"idempotency_key"`
}

// MetricCapacityGenerateInput 是按组织日历生成资源容量的有界命令。
type MetricCapacityGenerateInput struct {
	ResourceType   string   `json:"resource_type"`
	ResourceIDs    []string `json:"resource_ids"`
	OrganizationID string   `json:"organization_id"`
	From           string   `json:"from"`
	To             string   `json:"to"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// MetricCapacityGenerateResult 返回逐资源逐日的生成结果，不把跳过伪装成成功。
type MetricCapacityGenerateResult struct {
	ResourceType string            `json:"resource_type"`
	ResourceID   string            `json:"resource_id"`
	BusinessDate string            `json:"business_date"`
	Minutes      int64             `json:"minutes"`
	Status       string            `json:"status"`
	Message      string            `json:"message,omitempty"`
	Capacity     *ResourceCapacity `json:"capacity,omitempty"`
}

// MetricUsageCandidate 是从现场事件推算出的用量候选，不是已登记事实。
type MetricUsageCandidate struct {
	ServiceItemID   string    `json:"service_item_id"`
	ResourceType    string    `json:"resource_type"`
	ResourceID      string    `json:"resource_id"`
	IdentityID      string    `json:"identity_id,omitempty"`
	BusinessDate    string    `json:"business_date"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Minutes         int64     `json:"minutes"`
	Source          string    `json:"source"`
	SourceEventIDs  []string  `json:"source_event_ids"`
	Evidence        string    `json:"evidence"`
	Complete        bool      `json:"complete"`
	AutoConfirmable bool      `json:"auto_confirmable"`
	Reason          string    `json:"reason,omitempty"`
}

// MetricUsageCandidateApplyInput 是按服务项和日期应用一个现场候选的命令。
// 服务端会重新计算候选，不信任客户端提交的时段或事件 ID。
type MetricUsageCandidateApplyInput struct {
	ResourceID     string `json:"resource_id"`
	BusinessDate   string `json:"business_date"`
	IdempotencyKey string `json:"idempotency_key"`
}

// MetricAutoConfirmSetting 是租户级强证据自动确认策略，默认关闭。
type MetricAutoConfirmSetting struct {
	TenantID  string    `json:"-"`
	Enabled   bool      `json:"enabled"`
	Version   uint64    `json:"version"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// MetricAutoConfirmSettingInput 是策略写入命令。
type MetricAutoConfirmSettingInput struct {
	Enabled         bool   `json:"enabled"`
	ExpectedVersion uint64 `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

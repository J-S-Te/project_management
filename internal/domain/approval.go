package domain

import "time"

type ApprovalTask struct {
	ID                string    `json:"id"`
	Type              string    `json:"type"`
	ProjectID         string    `json:"project_id"`
	ServiceItemID     string    `json:"service_item_id"`
	Site              string    `json:"site"`
	Reason            string    `json:"reason"`
	RequesterUserID   string    `json:"requester_user_id"`
	ApproverUserID    string    `json:"approver_user_id"`
	CreatedAt         time.Time `json:"created_at"`
	Version           uint64    `json:"version"`
	ReportStatus      string    `json:"report_status"`
	ServiceItemStatus string    `json:"service_item_status"`
	Status            string    `json:"status"`
	CanDecide         bool      `json:"can_decide"`
	DecisionComment   string    `json:"decision_comment"`
	DecisionType      string    `json:"-"`
}

type ApprovalQuery struct {
	RequestID     string
	View          string
	Page          int
	PageSize      int
	UserID        string
	CanRollback   bool
	CanCorrection bool
}

type ApprovalPage struct {
	Items    []ApprovalTask `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

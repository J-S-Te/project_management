package domain

import "time"

type NativeScopeInput struct {
	OperationID            string            `json:"operation_id"`
	ExpectedProjectVersion uint64            `json:"expected_project_version"`
	BaseScopeVersion       uint64            `json:"base_scope_version"`
	Reason                 string            `json:"reason"`
	ServiceItems           []ContractService `json:"service_items"`
}

type NativeScopeDecision struct {
	OperationID     string `json:"operation_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Decision        string `json:"decision"`
	Comment         string `json:"comment"`
}

type NativeScopeChange struct {
	ID                     string        `json:"id"`
	ProjectID              string        `json:"project_id"`
	OperationID            string        `json:"operation_id"`
	BaseScopeVersion       uint64        `json:"base_scope_version"`
	ExpectedProjectVersion uint64        `json:"expected_project_version"`
	Version                uint64        `json:"version"`
	Status                 string        `json:"status"`
	Reason                 string        `json:"reason"`
	RequestedBy            string        `json:"requested_by"`
	DecisionOperationID    string        `json:"decision_operation_id,omitempty"`
	DecidedBy              string        `json:"decided_by,omitempty"`
	Comment                string        `json:"comment,omitempty"`
	ScopeHash              string        `json:"scope_hash"`
	AppliedScopeVersion    uint64        `json:"applied_scope_version,omitempty"`
	ServiceItems           []ServiceItem `json:"service_items"`
	CreatedAt              time.Time     `json:"created_at"`
	DecidedAt              *time.Time    `json:"decided_at,omitempty"`
}

type NativeScopeState struct {
	ProjectID      string              `json:"project_id"`
	ProjectVersion uint64              `json:"project_version"`
	ScopeVersion   uint64              `json:"scope_version"`
	ServiceItems   []ServiceItem       `json:"service_items"`
	Changes        []NativeScopeChange `json:"changes"`
	CanRequest     bool                `json:"can_request"`
	CanApprove     bool                `json:"can_approve"`
	ActorID        string              `json:"actor_id"`
}

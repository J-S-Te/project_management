package domain

import "time"

// ContractActivation is the idempotent contract-to-project handoff owned by the contract system.
type ContractActivation struct {
	ContractID      string            `json:"contract_id"`
	ContractVersion string            `json:"contract_version"`
	ContractName    string            `json:"contract_name"`
	Customer        string            `json:"customer"`
	EffectiveAt     time.Time         `json:"effective_at"`
	Services        []ContractService `json:"services"`
}

type ContractService struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	Site        string `json:"site"`
	Batch       string `json:"batch"`
	Category    string `json:"category"`
	System      string `json:"system"`
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
	Codes        []string  `json:"codes"`
	ValidFrom    time.Time `json:"valid_from"`
	ValidUntil   time.Time `json:"valid_until"`
	Status       string    `json:"status"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type DecompositionAdjustmentInput struct {
	Reason               string            `json:"reason"`
	SupplementContractID string            `json:"supplement_contract_id"`
	Items                []ContractService `json:"items"`
}

type AssignmentInput struct {
	TeamLeadID       string   `json:"team_lead_id"`
	ProjectManagerID string   `json:"project_manager_id"`
	EngineerIDs      []string `json:"engineer_ids"`
	EquipmentIDs     []string `json:"equipment_ids"`
	RequiredCodes    []string `json:"required_codes"`
	PlannedStart     string   `json:"planned_start"`
	PlannedEnd       string   `json:"planned_end"`
}

type TeamAssignmentInput struct {
	TeamLeadID string `json:"team_lead_id"`
}
type ExecutionAssignmentInput struct {
	ProjectManagerID string   `json:"project_manager_id"`
	EngineerIDs      []string `json:"engineer_ids"`
	EquipmentIDs     []string `json:"equipment_ids"`
	RequiredCodes    []string `json:"required_codes"`
}
type ImplementationPlanInput struct {
	PlannedStart        string `json:"planned_start"`
	PlannedEnd          string `json:"planned_end"`
	SitePlan            string `json:"site_plan"`
	PenetrationTestPlan string `json:"penetration_test_plan"`
}

type PreparationInput struct {
	EquipmentRequestID string `json:"equipment_request_id"`
	TravelRequestID    string `json:"travel_request_id"`
	Notes              string `json:"notes"`
}

type CheckInInput struct {
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	OccurredAt time.Time `json:"occurred_at"`
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

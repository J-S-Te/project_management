package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type ExpenseParticipant struct {
	TenantID   string    `json:"-"`
	ProjectID  string    `json:"project_id"`
	SubjectID  string    `json:"subject_id"`
	Status     string    `json:"status"`
	ValidFrom  time.Time `json:"valid_from"`
	ValidUntil time.Time `json:"valid_until"`
	GrantedBy  string    `json:"granted_by"`
	Reason     string    `json:"reason"`
	Version    uint64    `json:"version"`
}

type ExpenseApprover struct {
	NodeRole      string `json:"node_role"`
	SourceDigest  string `json:"source_digest,omitempty"`
	SourceOrgID   string `json:"source_org_id,omitempty"`
	SubjectID     string `json:"subject_id"`
	SourceField   string `json:"source_field"`
	SourceVersion uint64 `json:"source_version"`
}

type ExpenseProjectReference struct {
	ApproverReasonCodes  map[string][]string   `json:"approver_reason_codes,omitempty"`
	TravelSnapshot       ExpenseTravelSnapshot `json:"travel_snapshot"`
	ProjectID            string                `json:"project_id"`
	ProjectName          string                `json:"project_name"`
	SourceKind           string                `json:"source_kind"`
	ContractID           string                `json:"contract_id,omitempty"`
	CustomerID           string                `json:"customer_id,omitempty"`
	ServiceItemID        string                `json:"service_item_id,omitempty"`
	ServiceItemVersion   uint64                `json:"service_item_version,omitempty"`
	Site                 string                `json:"site,omitempty"`
	PlannedStart         string                `json:"planned_start,omitempty"`
	PlannedEnd           string                `json:"planned_end,omitempty"`
	ProjectStatus        string                `json:"project_status"`
	ServiceItemStatus    string                `json:"service_item_status,omitempty"`
	ParticipantEligible  bool                  `json:"participant_eligible"`
	Eligible             bool                  `json:"eligible"`
	EligibilitySource    string                `json:"eligibility_source"`
	RequiredApproverRefs []ExpenseApprover     `json:"required_approver_refs"`
	ProjectVersion       uint64                `json:"project_version"`
	ReferenceVersion     string                `json:"reference_version"`
	ValidatedAt          time.Time             `json:"validated_at"`
	ValidUntil           time.Time             `json:"valid_until"`
	ReasonCodes          []string              `json:"reason_codes"`
}

// ExpenseReference 区分当前参与资格和阶段障碍，仅从报告编制起开放正常项目报销。
// 参数：p为权威项目，item为可选服务项，actor为稳定主体，grant为当前名单，now为校验时刻。
// 返回值：最小引用；阶段不符或终止时不可报销，错误归属或失效资格不保留参与资格。
func ExpenseReference(p Project, item *ServiceItem, actor string, grant *ExpenseParticipant, now time.Time) ExpenseProjectReference {
	r := ExpenseProjectReference{ProjectID: p.ID, ProjectName: p.Name, SourceKind: p.SourceKind, ContractID: p.ContractID, CustomerID: p.CustomerID, ProjectVersion: p.Version, ProjectStatus: p.Status, ValidatedAt: now, ValidUntil: now.Add(2 * time.Minute), RequiredApproverRefs: []ExpenseApprover{}, ReasonCodes: []string{}}
	if actor != "" && strings.TrimSpace(actor) == actor {
		if actor == p.OwnerIdentityID || actor == p.ManagerIdentityID {
			r.ParticipantEligible = true
			r.EligibilitySource = "PROJECT_RESPONSIBILITY"
		}
		if grant != nil && grant.SubjectID == actor && grant.TenantID == p.TenantID && grant.ProjectID == p.ID && grant.Status == "ACTIVE" && !now.Before(grant.ValidFrom) && now.Before(grant.ValidUntil) {
			r.ParticipantEligible = true
			r.EligibilitySource = "EXPLICIT_PARTICIPANT"
		}
	}
	validItem := item == nil || item.ProjectID == p.ID && item.TenantID == p.TenantID
	if item != nil {
		r.ServiceItemID = item.ID
		r.ServiceItemVersion = item.Version
		r.ServiceItemStatus = item.Status
	}
	if !validItem {
		r.ParticipantEligible = false
		r.EligibilitySource = ""
		r.ReasonCodes = append(r.ReasonCodes, "SERVICE_ITEM_UNAVAILABLE")
	} else {
		expenseReferenceResponsibilities(&r, p, item, actor)
	}
	r.Eligible = r.ParticipantEligible
	if expenseTerminated(p.Status) {
		r.Eligible = false
		r.ReasonCodes = append(r.ReasonCodes, "PROJECT_TERMINATED")
	} else if p.Status != ProjectStatusReporting && p.Status != ProjectStatusCompleted {
		r.Eligible = false
		r.ReasonCodes = append(r.ReasonCodes, "PROJECT_REPORT_STAGE_REQUIRED")
	}
	if item != nil && validItem && expenseTerminated(item.Status) {
		r.Eligible = false
		r.ReasonCodes = append(r.ReasonCodes, "SERVICE_ITEM_TERMINATED")
	}
	if !r.ParticipantEligible && validItem {
		r.ReasonCodes = append(r.ReasonCodes, "ACTOR_NOT_ELIGIBLE")
	}
	if len(r.RequiredApproverRefs) == 0 {
		r.ApproverReasonCodes = map[string][]string{ExpenseNodeProjectOwner: {"PROJECT_EXPENSE_OWNER_MISSING"}}
	}
	if grant != nil && r.EligibilitySource == "EXPLICIT_PARTICIPANT" && grant.ValidUntil.Before(r.ValidUntil) {
		r.ValidUntil = grant.ValidUntil
	}
	version := struct {
		Project                       Project
		Item                          *ServiceItem
		Grant                         *ExpenseParticipant
		Actor, EligibilitySource      string
		ParticipantEligible, Eligible bool
	}{p, item, grant, actor, r.EligibilitySource, r.ParticipantEligible, r.Eligible}
	b, _ := json.Marshal(version)
	sum := sha256.Sum256(b)
	r.ReferenceVersion = hex.EncodeToString(sum[:])
	return r
}

// expenseTerminated 统一识别当前项目状态及兼容旧英文终止值。
// 参数：status为权威持久化状态。
// 返回值：是否为终止；不把报告归档当作终止。
func expenseTerminated(status string) bool {
	return status == ProjectStatusTerminated || strings.EqualFold(strings.TrimSpace(status), "terminated")
}

// expenseReferenceResponsibilities 在正确项目边界内派生当前责任资格及费用审批引用。
// 参数：r为最小引用，p/item为权威对象，actor为稳定主体。
// 返回值：无；只读取身份字段，不按展示名匹配，也不扩展交付权限。
func expenseReferenceResponsibilities(r *ExpenseProjectReference, p Project, item *ServiceItem, actor string) {
	approvers := []ExpenseApprover{{NodeRole: ExpenseNodeProjectOwner, SubjectID: p.ManagerIdentityID, SourceField: "manager_identity_id", SourceVersion: p.Version}}
	if item != nil {
		r.Site = item.Site
		r.PlannedStart = item.PlannedStart
		r.PlannedEnd = item.PlannedEnd
		if actor != "" && strings.TrimSpace(actor) == actor && (actor == item.TeamLeadID || actor == item.ProjectManagerID || slices.Contains(item.EngineerIDs, actor)) {
			r.ParticipantEligible = true
			r.EligibilitySource = "SERVICE_ITEM_RESPONSIBILITY"
		}
		approvers = []ExpenseApprover{{NodeRole: ExpenseNodeProjectOwner, SubjectID: item.ProjectManagerID, SourceField: "project_manager_id", SourceVersion: item.Version}}
	}
	for _, approver := range approvers {
		if approver.SubjectID != "" {
			r.RequiredApproverRefs = append(r.RequiredApproverRefs, approver)
		}
	}
}

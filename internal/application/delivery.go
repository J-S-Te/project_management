package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

const (
	EventContractActivated       = "CONTRACT_ACTIVATED"
	EventContractStampStatus     = "CONTRACT_STAMP_STATUS_SYNCED"
	EventDecompositionAdjusted   = "DECOMPOSITION_ADJUSTED"
	EventAssignmentPublished     = "ASSIGNMENT_PUBLISHED"
	EventTeamAssigned            = "TEAM_ASSIGNED"
	EventExecutionTeamAssigned   = "EXECUTION_TEAM_ASSIGNED"
	EventImplementationPlanned   = "IMPLEMENTATION_PLANNED"
	EventPreparationStarted      = "PREPARATION_STARTED"
	EventFieldCheckIn            = "FIELD_CHECK_IN"
	EventFieldRecordSubmitted    = "FIELD_RECORD_SUBMITTED"
	EventDeviationReported       = "DEVIATION_REPORTED"
	EventDeviationReviewed       = "DEVIATION_REVIEWED"
	EventFieldImplementationDone = "FIELD_IMPLEMENTATION_COMPLETED"
	EventSpecialMethodReviewed   = "SPECIAL_METHOD_REVIEWED"
	EventReportStatusUpdated     = "REPORT_STATUS_UPDATED"
)

type DeliveryRepository interface {
	FindProjectByContractVersion(context.Context, platform.ScopeFilter, string, string) (domain.Project, error)
	ActivateContract(context.Context, domain.Project, []domain.ServiceItem, domain.DeliveryEvent) error
	SyncContractStampStatus(context.Context, domain.Project, bool, domain.DeliveryEvent) error
	ApplyDeliveryEvent(context.Context, domain.DeliveryEvent) error
	ListDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error)
	FindProjectForDeviation(context.Context, platform.ScopeFilter, string) (string, string, error)
	UpsertCapability(context.Context, domain.Capability, string) (domain.Capability, error)
	ListCapabilities(context.Context, string, string) ([]domain.Capability, error)
	FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error)
}

func (s *Service) deliveryRepo() (DeliveryRepository, error) {
	repo, ok := s.Repo.(DeliveryRepository)
	if !ok {
		return nil, errors.New("delivery repository unavailable")
	}
	return repo, nil
}

func (s *Service) ActivateContract(ctx context.Context, p platform.Principal, input domain.ContractActivation) (domain.Project, error) {
	filter, scopeErr := authorizeProjectScope(p, "project.contract.import")
	if scopeErr != nil {
		return domain.Project{}, scopeErr
	}
	if strings.TrimSpace(input.ContractID) == "" || strings.TrimSpace(input.ContractVersion) == "" || strings.TrimSpace(input.Customer) == "" || len(input.Services) == 0 || input.EffectiveAt.IsZero() {
		return domain.Project{}, ErrValidation
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.Project{}, err
	}
	if existing, findErr := repo.FindProjectByContractVersion(ctx, filter, strings.TrimSpace(input.ContractID), strings.TrimSpace(input.ContractVersion)); findErr == nil {
		event := deliveryEvent(p, existing.ID, "", EventContractStampStatus, map[string]any{"contract_id": existing.Contract, "contract_version": existing.ContractVersion, "stamped_contract_uploaded": input.StampedContractUploaded})
		if err := repo.SyncContractStampStatus(ctx, existing, input.StampedContractUploaded, event); err != nil {
			return domain.Project{}, err
		}
		if input.StampedContractUploaded {
			existing.Health = "正常"
		} else {
			existing.Health = "关注"
		}
		return existing, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return domain.Project{}, findErr
	}
	now := time.Now().UTC()
	health := "关注"
	if input.StampedContractUploaded {
		health = "正常"
	}
	ownerIdentityID := firstNonEmpty(p.IdentityID, p.UserID)
	ownerOrgID := ""
	if len(filter.OrganizationIDs) == 1 {
		ownerOrgID = filter.OrganizationIDs[0]
	}
	if !filter.AllowAll && !filter.AllowSelf && ownerOrgID == "" {
		return domain.Project{}, ErrForbidden
	}
	project := domain.Project{TenantID: p.TenantID, OwnerOrgID: ownerOrgID, OwnerIdentityID: ownerIdentityID, ID: projectID(now), Name: firstNonEmpty(input.ContractName, input.ContractID), Customer: strings.TrimSpace(input.Customer), Contract: strings.TrimSpace(input.ContractID), ContractVersion: strings.TrimSpace(input.ContractVersion), Status: "待拆解确认", Health: health, Team: "未分配", Manager: "—", SupplementStatus: "NONE", CreatedAt: now, UpdatedAt: now}
	grouped, groupErr := groupContractServices(input.Services)
	if groupErr != nil {
		return domain.Project{}, groupErr
	}
	items := make([]domain.ServiceItem, 0, len(grouped))
	for index, source := range grouped {
		if strings.TrimSpace(source.SourceID) == "" || strings.TrimSpace(source.Site) == "" || strings.TrimSpace(source.Batch) == "" || strings.TrimSpace(source.Category) == "" {
			return domain.Project{}, ErrValidation
		}
		mode := strings.ToUpper(strings.TrimSpace(source.TestMode))
		if mode == "" {
			mode = "STANDARD"
		}
		if mode != "STANDARD" && mode != "PENETRATION" {
			return domain.Project{}, ErrValidation
		}
		items = append(items, domain.ServiceItem{TenantID: p.TenantID, ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(project.ID, "PJ-"), index+1), ProjectID: project.ID, SourceServiceID: strings.TrimSpace(source.SourceID), Batch: strings.TrimSpace(source.Batch), Site: strings.TrimSpace(source.Site), Category: strings.TrimSpace(source.Category), Requirement: strings.TrimSpace(source.Requirement), System: strings.TrimSpace(source.System), SystemLevel: strings.TrimSpace(source.SystemLevel), Special: yesNo(mode == "PENETRATION"), TestMode: mode, Status: "待确认", ConflictStatus: "UNCHECKED"})
	}
	project.Services = len(items)
	event := deliveryEvent(p, project.ID, "", EventContractActivated, map[string]any{"contract_id": project.Contract, "contract_version": project.ContractVersion, "effective_at": input.EffectiveAt, "service_count": len(items), "stamped_contract_uploaded": input.StampedContractUploaded})
	if err := repo.ActivateContract(ctx, project, items, event); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (s *Service) AdjustDecomposition(ctx context.Context, p platform.Principal, projectID string, input domain.DecompositionAdjustmentInput) error {
	if err := s.authorizeProject(ctx, p, "project.decomposition.manage", projectID); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.SupplementContractID) == "" {
		return ErrValidation
	}
	items := make([]domain.ServiceItem, 0, len(input.Items))
	for index, source := range input.Items {
		if source.SourceID == "" || source.Site == "" || source.Batch == "" || source.Category == "" {
			return ErrValidation
		}
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if mode != "STANDARD" && mode != "PENETRATION" {
			return ErrValidation
		}
		items = append(items, domain.ServiceItem{ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(projectID, "PJ-"), index+1), ProjectID: projectID, SourceServiceID: source.SourceID, Batch: source.Batch, Site: source.Site, Category: source.Category, Requirement: source.Requirement, System: source.System, SystemLevel: source.SystemLevel, TestMode: mode, Special: yesNo(mode == "PENETRATION"), Status: "待确认", ConflictStatus: "UNCHECKED"})
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventDecompositionAdjusted, map[string]any{"reason": strings.TrimSpace(input.Reason), "supplement_contract_id": strings.TrimSpace(input.SupplementContractID), "service_items": items}))
}

func (s *Service) AssignServiceItem(ctx context.Context, p platform.Principal, itemID string, input domain.AssignmentInput) (domain.ConflictCheckResult, error) {
	if err := s.authorizeServiceItem(ctx, p, "project.resource.assign", itemID); err != nil {
		return domain.ConflictCheckResult{}, err
	}
	if strings.TrimSpace(input.TeamLeadID) == "" || strings.TrimSpace(input.ProjectManagerID) == "" || len(input.EngineerIDs) == 0 || input.PlannedStart == "" || input.PlannedEnd == "" {
		return domain.ConflictCheckResult{}, ErrValidation
	}
	start, e1 := time.Parse(time.RFC3339, input.PlannedStart)
	end, e2 := time.Parse(time.RFC3339, input.PlannedEnd)
	if e1 != nil || e2 != nil || !end.After(start) {
		return domain.ConflictCheckResult{}, ErrValidation
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	resourceIDs := append(append([]string{}, input.EngineerIDs...), input.EquipmentIDs...)
	capabilities, err := repo.FindCapabilities(ctx, p.TenantID, time.Now().UTC().Format(time.RFC3339), resourceIDs)
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	result := checkCapabilities(input.RequiredCodes, resourceIDs, capabilities)
	payload := map[string]any{"team_lead_id": input.TeamLeadID, "project_manager_id": input.ProjectManagerID, "engineer_ids": input.EngineerIDs, "equipment_ids": input.EquipmentIDs, "required_codes": input.RequiredCodes, "planned_start": input.PlannedStart, "planned_end": input.PlannedEnd, "conflict_status": map[bool]string{true: "PASSED", false: "CONFLICT"}[result.Passed], "conflicts": result.Conflicts}
	if err := s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventAssignmentPublished, payload)); err != nil {
		return domain.ConflictCheckResult{}, err
	}
	return result, nil
}

func (s *Service) AssignTeam(ctx context.Context, p platform.Principal, itemID string, input domain.TeamAssignmentInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.team.assign", itemID); err != nil {
		return err
	}
	if strings.TrimSpace(input.TeamLeadID) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventTeamAssigned, map[string]any{"team_lead_id": input.TeamLeadID}))
}
func (s *Service) AssignExecutionTeam(ctx context.Context, p platform.Principal, itemID string, input domain.ExecutionAssignmentInput) (domain.ConflictCheckResult, error) {
	filter, err := authorizeProjectScope(p, "project.execution.assign")
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	if _, err := s.Repo.GetServiceItem(ctx, filter, itemID); err != nil {
		return domain.ConflictCheckResult{}, err
	}
	if input.ProjectManagerID == "" || len(input.EngineerIDs) == 0 {
		return domain.ConflictCheckResult{}, ErrValidation
	}
	required := append([]string{}, input.RequiredCodes...)
	items, err := s.Repo.ListServiceItems(ctx, filter, "")
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	found := false
	for _, item := range items {
		if item.ID == itemID {
			found = true
			if item.TestMode == "PENETRATION" {
				required = append(required, "PENETRATION_TEST")
			}
			break
		}
	}
	if !found {
		return domain.ConflictCheckResult{}, ErrNotFound
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	ids := append(append([]string{}, input.EngineerIDs...), input.EquipmentIDs...)
	caps, err := repo.FindCapabilities(ctx, p.TenantID, time.Now().UTC().Format(time.RFC3339), ids)
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	result := checkCapabilities(required, ids, caps)
	payload := map[string]any{"project_manager_id": input.ProjectManagerID, "engineer_ids": input.EngineerIDs, "equipment_ids": input.EquipmentIDs, "required_codes": required, "conflict_status": map[bool]string{true: "PASSED", false: "CONFLICT"}[result.Passed], "conflicts": result.Conflicts}
	return result, s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventExecutionTeamAssigned, payload))
}
func (s *Service) PlanImplementation(ctx context.Context, p platform.Principal, itemID string, input domain.ImplementationPlanInput) error {
	filter, err := authorizeProjectScope(p, "project.implementation.plan")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	// 先判前置状态再判字段：两者返回的错误语义不同，前端提示也不同。
	// 前置状态不满足时用户改表单也没用，必须先去完成缺的那一步。
	if err := CheckImplementationPlanPrecondition(PlanPrecondition{
		Status: item.Status, ProjectManagerID: item.ProjectManagerID,
		ConflictStatus: item.ConflictStatus, Special: item.Special, TechReviewStatus: item.TechReviewStatus,
	}); err != nil {
		return err
	}
	start, e1 := time.Parse(time.RFC3339, input.PlannedStart)
	end, e2 := time.Parse(time.RFC3339, input.PlannedEnd)
	if e1 != nil || e2 != nil {
		return ValidationError("计划开始与计划结束必须是有效的日期时间")
	}
	if !end.After(start) {
		return ValidationError("计划结束时间必须晚于计划开始时间")
	}
	if strings.TrimSpace(input.SitePlan) == "" {
		return ValidationError("请填写现场计划")
	}
	if item.TestMode == "PENETRATION" {
		if err := validatePenetrationCompliance(input); err != nil {
			return err
		}
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventImplementationPlanned, map[string]any{
		"planned_start":         input.PlannedStart,
		"planned_end":           input.PlannedEnd,
		"site_plan":             input.SitePlan,
		"penetration_test_plan": input.PenetrationTestPlan,
		"auth_doc_no":           strings.TrimSpace(input.AuthDocNo),
		"auth_start":            strings.TrimSpace(input.AuthStart),
		"auth_end":              strings.TrimSpace(input.AuthEnd),
		"auth_scope":            strings.TrimSpace(input.AuthScope),
		"test_scope":            strings.TrimSpace(input.TestScope),
		"test_window":           strings.TrimSpace(input.TestWindow),
		"emergency_contact":     strings.TrimSpace(input.EmergencyContact),
		"rollback_plan":         strings.TrimSpace(input.RollbackPlan),
	}))
}

// PlanPrecondition 是"发布实施计划"所需的最小服务项状态快照。
// 服务层用领域对象填充它，仓储层在行锁内用记录填充它，保证两处判定同一套规则。
type PlanPrecondition struct {
	Status           string
	ProjectManagerID string
	ConflictStatus   string
	Special          string
	TechReviewStatus string
}

// CheckImplementationPlanPrecondition 判定服务项是否已走到"发布实施计划"这一步。
// 应用层与仓储层共用它，避免两处守卫各自演化；同时把模糊的 422 变成可执行的指引。
func CheckImplementationPlanPrecondition(item PlanPrecondition) error {
	switch strings.TrimSpace(item.Status) {
	case "待分配", "待制定计划":
	default:
		return PreconditionError(fmt.Sprintf("服务项当前状态为「%s」，不能发布实施计划。", strings.TrimSpace(item.Status)))
	}
	if strings.TrimSpace(item.ProjectManagerID) == "" {
		return PreconditionError("请先在「任务分配」中指派项目经理。")
	}
	if item.ConflictStatus != "PASSED" {
		return PreconditionError(fmt.Sprintf("请先在「任务分配」中完成能力校验（当前状态：%s）。", conflictStatusLabel(item.ConflictStatus)))
	}
	if item.Special == "是" && item.TechReviewStatus != "APPROVED" {
		return PreconditionError("该服务项为特殊方法，需先通过技术总监复核后才能发布实施计划。")
	}
	return nil
}

// conflictStatusLabel 把排期与能力校验状态翻译成用户能理解的说明。
func conflictStatusLabel(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PASSED":
		return "校验通过"
	case "CONFLICT":
		return "存在冲突"
	case "", "UNCHECKED":
		return "尚未校验"
	default:
		return status
	}
}

// validatePenetrationCompliance 渗透测试专项合规要素：必须提供授权书编号、授权范围、
// 计划测试范围、测试时间窗、应急联系人与回滚方案，且授权有效期内才能发布计划。
// 逐项指出缺失字段，避免用户只看到笼统的"请求参数不合法"。
func validatePenetrationCompliance(input domain.ImplementationPlanInput) error {
	required := []struct {
		value string
		label string
	}{
		{input.PenetrationTestPlan, "渗透测试专项计划"},
		{input.AuthDocNo, "授权书编号"},
		{input.AuthScope, "授权范围"},
		{input.TestScope, "计划测试范围"},
		{input.TestWindow, "测试时间窗"},
		{input.EmergencyContact, "应急联系人"},
		{input.RollbackPlan, "回滚方案"},
	}
	missing := make([]string, 0, len(required))
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.label)
		}
	}
	if len(missing) > 0 {
		return ValidationError("请补充渗透测试专项合规要素：" + strings.Join(missing, "、"))
	}
	authStart, e1 := time.Parse(time.RFC3339, input.AuthStart)
	authEnd, e2 := time.Parse(time.RFC3339, input.AuthEnd)
	if e1 != nil || e2 != nil {
		return ValidationError("授权生效与授权截止必须是有效的日期时间")
	}
	if !authEnd.After(authStart) {
		return ValidationError("授权截止时间必须晚于授权生效时间")
	}
	return nil
}

// ReviewSpecialMethod 技术总监对特殊方法（渗透测试专项）服务项的适用性与风险控制复核。
// 复核通过后才能发布实施计划；驳回后可在修正后再次提交复核。
func (s *Service) ReviewSpecialMethod(ctx context.Context, p platform.Principal, itemID string, input domain.SpecialMethodReviewInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.special_method.review", itemID); err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "APPROVED" && decision != "REJECTED" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventSpecialMethodReviewed, map[string]any{"decision": decision, "comment": strings.TrimSpace(input.Comment)}))
}

// UpdateReportStatus 服务项报告从编制中逐级推进：编制中→已审核→已签发→已归档。
func (s *Service) UpdateReportStatus(ctx context.Context, p platform.Principal, itemID string, input domain.ReportStatusInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.complete", itemID); err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventReportStatusUpdated, map[string]any{"phase": strings.ToUpper(strings.TrimSpace(input.Phase))}))
}

func (s *Service) StartPreparation(ctx context.Context, p platform.Principal, itemID string, input domain.PreparationInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.implementation.plan", itemID); err != nil {
		return err
	}
	if strings.TrimSpace(input.EquipmentRequestID) == "" || strings.TrimSpace(input.TravelRequestID) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventPreparationStarted, map[string]any{"equipment_request_id": input.EquipmentRequestID, "travel_request_id": input.TravelRequestID, "notes": input.Notes}))
}

func (s *Service) CheckIn(ctx context.Context, p platform.Principal, itemID string, input domain.CheckInInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.execute", itemID); err != nil {
		return err
	}
	if input.Latitude < -90 || input.Latitude > 90 || input.Longitude < -180 || input.Longitude > 180 || input.OccurredAt.IsZero() || time.Since(input.OccurredAt) > 24*time.Hour || time.Until(input.OccurredAt) > 5*time.Minute {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldCheckIn, map[string]any{"latitude": input.Latitude, "longitude": input.Longitude, "occurred_at": input.OccurredAt}))
}

func (s *Service) SubmitFieldRecord(ctx context.Context, p platform.Principal, itemID string, input domain.FieldRecordInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.execute", itemID); err != nil {
		return err
	}
	if strings.TrimSpace(input.RawData) == "" || strings.TrimSpace(input.Environment) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldRecordSubmitted, map[string]any{"raw_data": input.RawData, "environment": input.Environment, "evidence_urls": input.EvidenceURLs}))
}

func (s *Service) ReportDeviation(ctx context.Context, p platform.Principal, itemID string, input domain.DeviationInput) (string, error) {
	if err := s.authorizeServiceItem(ctx, p, "project.deviation.report", itemID); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Description) == "" {
		return "", ErrValidation
	}
	id := "DV-" + ulid.Make().String()
	return id, s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventDeviationReported, map[string]any{"deviation_id": id, "description": input.Description, "severity": input.Severity, "evidence_url": input.EvidenceURL, "decision": "PENDING"}))
}

func (s *Service) ReviewDeviation(ctx context.Context, p platform.Principal, deviationID string, input domain.DeviationReviewInput) error {
	filter, err := authorizeProjectScope(p, "project.deviation.review")
	if err != nil {
		return err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	projectID, itemID, err := repo.FindProjectForDeviation(ctx, filter, deviationID)
	if err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "RELEASE" && decision != "TERMINATE" && decision != "RETEST" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, itemID, EventDeviationReviewed, map[string]any{"deviation_id": deviationID, "decision": decision, "comment": input.Comment}))
}

func (s *Service) CompleteFieldImplementation(ctx context.Context, p platform.Principal, projectID string) error {
	if err := s.authorizeProject(ctx, p, "project.field.complete", projectID); err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventFieldImplementationDone, map[string]any{"confirmed_by": p.UserID}))
}

func (s *Service) ListDeliveryEvents(ctx context.Context, p platform.Principal, projectID string) ([]domain.DeliveryEvent, error) {
	filter, scopeErr := authorizeProjectScope(p, "project.read")
	if scopeErr != nil {
		return nil, scopeErr
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	return repo.ListDeliveryEvents(ctx, filter, projectID)
}
// CapabilityImportResult 汇总一次能力记录导出的导入结果。
type CapabilityImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

func validateCapability(item domain.Capability) error {
	if (item.ResourceType != "PERSON" && item.ResourceType != "EQUIPMENT") || strings.TrimSpace(item.ResourceID) == "" || strings.TrimSpace(item.ResourceName) == "" || len(item.Codes) == 0 {
		return ErrValidation
	}
	return nil
}

// resourceIDPrefix 区分人员与设备的资源编号前缀，避免两类编号混用。
func resourceIDPrefix(resourceType string) string {
	if resourceType == "EQUIPMENT" {
		return "EQ-"
	}
	return "P-"
}

// nextResourceID 依据现有记录为人员/设备生成下一个可用编号（P-0001 / EQ-0001）。
// 已占用的编号会被跳过，兼容历史手工编号与同批次导入。
func nextResourceID(existing []domain.Capability, resourceType string) string {
	prefix := resourceIDPrefix(resourceType)
	used := map[string]bool{}
	maxSequence := 0
	for _, item := range existing {
		if item.ResourceType != resourceType {
			continue
		}
		used[item.ResourceID] = true
		if !strings.HasPrefix(item.ResourceID, prefix) {
			continue
		}
		if sequence, err := strconv.Atoi(strings.TrimPrefix(item.ResourceID, prefix)); err == nil && sequence > maxSequence {
			maxSequence = sequence
		}
	}
	for {
		maxSequence++
		candidate := fmt.Sprintf("%s%04d", prefix, maxSequence)
		if !used[candidate] {
			return candidate
		}
	}
}

// assignResourceID 在调用方未提供编号时自动生成，保证人员与设备编号各自独立且可读。
func assignResourceID(existing []domain.Capability, item *domain.Capability) {
	if strings.TrimSpace(item.ResourceID) != "" {
		return
	}
	if item.ResourceType != "PERSON" && item.ResourceType != "EQUIPMENT" {
		return
	}
	item.ResourceID = nextResourceID(existing, item.ResourceType)
}

func (s *Service) UpsertCapability(ctx context.Context, p platform.Principal, item domain.Capability) (domain.Capability, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return item, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return item, e
	}
	item.TenantID = p.TenantID
	if strings.TrimSpace(item.ResourceID) == "" {
		existing, err := repo.ListCapabilities(ctx, p.TenantID, item.ResourceType)
		if err != nil {
			return item, err
		}
		assignResourceID(existing, &item)
	}
	if err := validateCapability(item); err != nil {
		return item, err
	}
	item.Status = firstNonEmpty(item.Status, "ACTIVE")
	return repo.UpsertCapability(ctx, item, p.UserID)
}

// ImportCapabilities 批量写入能力记录（人员资质或设备能力）。逐行校验并独立写入，
// 单行失败只累计跳过原因，不影响其余行，避免一条脏数据阻塞整批导入。
func (s *Service) ImportCapabilities(ctx context.Context, p platform.Principal, rows []domain.Capability) (CapabilityImportResult, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return CapabilityImportResult{}, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return CapabilityImportResult{}, e
	}
	result := CapabilityImportResult{}
	// 导入同样支持编号留空：按类型顺延生成，且本批次内逐行消耗，避免整批手工编号。
	known, err := repo.ListCapabilities(ctx, p.TenantID, "")
	if err != nil {
		return CapabilityImportResult{}, err
	}
	for i := range rows {
		line := fmt.Sprintf("数据行 %d", i+1)
		assignResourceID(known, &rows[i])
		if err := validateCapability(rows[i]); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, line+": 资源类型、编号、名称或能力码不完整")
			continue
		}
		known = append(known, rows[i])
		rows[i].TenantID = p.TenantID
		rows[i].Status = firstNonEmpty(rows[i].Status, "ACTIVE")
		if _, err := repo.UpsertCapability(ctx, rows[i], p.UserID); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, line+": "+err.Error())
			continue
		}
		result.Imported++
	}
	return result, nil
}
func (s *Service) ListCapabilities(ctx context.Context, p platform.Principal, typ string) ([]domain.Capability, error) {
	if err := requireDirectoryRead(p, "project.read", "project.resource.read"); err != nil {
		return nil, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	return repo.ListCapabilities(ctx, p.TenantID, typ)
}

func (s *Service) ListEquipment(ctx context.Context, p platform.Principal) ([]domain.Capability, error) {
	if err := requireDirectoryRead(p, "project.read", "project.device.read"); err != nil {
		return nil, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	return repo.ListCapabilities(ctx, p.TenantID, "EQUIPMENT")
}

func (s *Service) UpsertEquipment(ctx context.Context, p platform.Principal, item domain.Capability) (domain.Capability, error) {
	if err := requireApplicationAuthorization(p, "project.device.manage"); err != nil {
		return item, err
	}
	if item.ResourceType != "EQUIPMENT" || strings.TrimSpace(item.ResourceID) == "" || strings.TrimSpace(item.ResourceName) == "" || len(item.Codes) == 0 {
		return item, ErrValidation
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return item, e
	}
	item.TenantID = p.TenantID
	item.Status = firstNonEmpty(item.Status, "ACTIVE")
	return repo.UpsertCapability(ctx, item, p.UserID)
}
func (s *Service) applyEvent(ctx context.Context, event domain.DeliveryEvent) error {
	repo, e := s.deliveryRepo()
	if e != nil {
		return e
	}
	return repo.ApplyDeliveryEvent(ctx, event)
}

func (s *Service) authorizeProject(ctx context.Context, p platform.Principal, permission, projectID string) error {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	_, err = s.Repo.GetProject(ctx, filter, projectID)
	return err
}

func (s *Service) authorizeServiceItem(ctx context.Context, p platform.Principal, permission, itemID string) error {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	_, err = s.Repo.GetServiceItem(ctx, filter, itemID)
	return err
}
func deliveryEvent(p platform.Principal, projectID, itemID, typ string, payload map[string]any) domain.DeliveryEvent {
	return domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: p.TenantID, ProjectID: projectID, ServiceItemID: itemID, Type: typ, ActorUserID: p.UserID, Payload: payload, CreatedAt: time.Now().UTC()}
}
func projectID(now time.Time) string {
	return "PJ-" + now.Format("2006") + "-" + strings.ToUpper(ulid.Make().String()[20:])
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
func groupContractServices(sources []domain.ContractService) ([]domain.ContractService, error) {
	seen := map[string]bool{}
	groups := map[string]domain.ContractService{}
	keys := []string{}
	for _, source := range sources {
		source.SourceID = strings.TrimSpace(source.SourceID)
		source.Site = strings.TrimSpace(source.Site)
		source.Batch = strings.TrimSpace(source.Batch)
		source.Category = strings.TrimSpace(source.Category)
		if source.SourceID == "" || source.Site == "" || source.Batch == "" || source.Category == "" || seen[source.SourceID] {
			return nil, ErrValidation
		}
		seen[source.SourceID] = true
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if mode != "STANDARD" && mode != "PENETRATION" {
			return nil, ErrValidation
		}
		key := source.Site + "\x00" + source.Batch + "\x00" + source.Category + "\x00" + mode
		if current, ok := groups[key]; ok {
			current.SourceID = joinUnique(current.SourceID, source.SourceID)
			current.Requirement = joinUnique(current.Requirement, strings.TrimSpace(source.Requirement))
			current.System = joinUnique(current.System, strings.TrimSpace(source.System))
			current.SystemLevel = joinUnique(current.SystemLevel, strings.TrimSpace(source.SystemLevel))
			current.Name = joinUnique(current.Name, strings.TrimSpace(source.Name))
			groups[key] = current
		} else {
			source.TestMode = mode
			groups[key] = source
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]domain.ContractService, 0, len(keys))
	for _, key := range keys {
		out = append(out, groups[key])
	}
	return out, nil
}
func joinUnique(existing, next string) string {
	if next == "" {
		return existing
	}
	for _, part := range strings.Split(existing, "、") {
		if part == next {
			return existing
		}
	}
	if existing == "" {
		return next
	}
	return existing + "、" + next
}
func checkCapabilities(required, resources []string, items []domain.Capability) domain.ConflictCheckResult {
	covered := map[string]bool{}
	active := map[string]bool{}
	for _, c := range items {
		active[c.ResourceID] = c.Status == "ACTIVE"
		for _, code := range c.Codes {
			if active[c.ResourceID] {
				covered[code] = true
			}
		}
	}
	conflicts := []string{}
	for _, id := range resources {
		if !active[id] {
			conflicts = append(conflicts, "资源 "+id+" 缺少有效资质/能力记录")
		}
	}
	sort.Strings(required)
	for _, code := range required {
		if !covered[code] {
			conflicts = append(conflicts, "缺少能力："+code)
		}
	}
	return domain.ConflictCheckResult{Passed: len(conflicts) == 0, Conflicts: conflicts}
}

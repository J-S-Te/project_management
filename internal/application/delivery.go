package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
)

type DeliveryRepository interface {
	FindProjectByContractVersion(context.Context, string, string, string) (domain.Project, error)
	ActivateContract(context.Context, domain.Project, []domain.ServiceItem, domain.DeliveryEvent) error
	SyncContractStampStatus(context.Context, domain.Project, bool, domain.DeliveryEvent) error
	ApplyDeliveryEvent(context.Context, domain.DeliveryEvent) error
	ListDeliveryEvents(context.Context, string, string) ([]domain.DeliveryEvent, error)
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
	if strings.TrimSpace(input.ContractID) == "" || strings.TrimSpace(input.ContractVersion) == "" || strings.TrimSpace(input.Customer) == "" || len(input.Services) == 0 || input.EffectiveAt.IsZero() {
		return domain.Project{}, ErrValidation
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.Project{}, err
	}
	if existing, findErr := repo.FindProjectByContractVersion(ctx, p.TenantID, strings.TrimSpace(input.ContractID), strings.TrimSpace(input.ContractVersion)); findErr == nil {
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
	project := domain.Project{TenantID: p.TenantID, ID: projectID(now), Name: firstNonEmpty(input.ContractName, input.ContractID), Customer: strings.TrimSpace(input.Customer), Contract: strings.TrimSpace(input.ContractID), ContractVersion: strings.TrimSpace(input.ContractVersion), Status: "待拆解确认", Health: health, Team: "未分配", Manager: "—", SupplementStatus: "NONE", CreatedAt: now, UpdatedAt: now}
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
		items = append(items, domain.ServiceItem{TenantID: p.TenantID, ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(project.ID, "PJ-"), index+1), ProjectID: project.ID, SourceServiceID: strings.TrimSpace(source.SourceID), Batch: strings.TrimSpace(source.Batch), Site: strings.TrimSpace(source.Site), Category: strings.TrimSpace(source.Category), Requirement: strings.TrimSpace(source.Requirement), System: strings.TrimSpace(source.System), Special: yesNo(mode == "PENETRATION"), TestMode: mode, Status: "待确认", ConflictStatus: "UNCHECKED"})
	}
	project.Services = len(items)
	event := deliveryEvent(p, project.ID, "", EventContractActivated, map[string]any{"contract_id": project.Contract, "contract_version": project.ContractVersion, "effective_at": input.EffectiveAt, "service_count": len(items), "stamped_contract_uploaded": input.StampedContractUploaded})
	if err := repo.ActivateContract(ctx, project, items, event); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (s *Service) AdjustDecomposition(ctx context.Context, p platform.Principal, projectID string, input domain.DecompositionAdjustmentInput) error {
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
		items = append(items, domain.ServiceItem{ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(projectID, "PJ-"), index+1), ProjectID: projectID, SourceServiceID: source.SourceID, Batch: source.Batch, Site: source.Site, Category: source.Category, Requirement: source.Requirement, System: source.System, TestMode: mode, Special: yesNo(mode == "PENETRATION"), Status: "待确认", ConflictStatus: "UNCHECKED"})
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventDecompositionAdjusted, map[string]any{"reason": strings.TrimSpace(input.Reason), "supplement_contract_id": strings.TrimSpace(input.SupplementContractID), "service_items": items}))
}

func (s *Service) AssignServiceItem(ctx context.Context, p platform.Principal, itemID string, input domain.AssignmentInput) (domain.ConflictCheckResult, error) {
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
	if strings.TrimSpace(input.TeamLeadID) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventTeamAssigned, map[string]any{"team_lead_id": input.TeamLeadID}))
}
func (s *Service) AssignExecutionTeam(ctx context.Context, p platform.Principal, itemID string, input domain.ExecutionAssignmentInput) (domain.ConflictCheckResult, error) {
	if input.ProjectManagerID == "" || len(input.EngineerIDs) == 0 {
		return domain.ConflictCheckResult{}, ErrValidation
	}
	required := append([]string{}, input.RequiredCodes...)
	items, err := s.Repo.ListServiceItems(ctx, p.TenantID, "")
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
	start, e1 := time.Parse(time.RFC3339, input.PlannedStart)
	end, e2 := time.Parse(time.RFC3339, input.PlannedEnd)
	if e1 != nil || e2 != nil || !end.After(start) || strings.TrimSpace(input.SitePlan) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventImplementationPlanned, map[string]any{"planned_start": input.PlannedStart, "planned_end": input.PlannedEnd, "site_plan": input.SitePlan, "penetration_test_plan": input.PenetrationTestPlan}))
}

func (s *Service) StartPreparation(ctx context.Context, p platform.Principal, itemID string, input domain.PreparationInput) error {
	if strings.TrimSpace(input.EquipmentRequestID) == "" || strings.TrimSpace(input.TravelRequestID) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventPreparationStarted, map[string]any{"equipment_request_id": input.EquipmentRequestID, "travel_request_id": input.TravelRequestID, "notes": input.Notes}))
}

func (s *Service) CheckIn(ctx context.Context, p platform.Principal, itemID string, input domain.CheckInInput) error {
	if input.Latitude < -90 || input.Latitude > 90 || input.Longitude < -180 || input.Longitude > 180 || input.OccurredAt.IsZero() || time.Since(input.OccurredAt) > 24*time.Hour || time.Until(input.OccurredAt) > 5*time.Minute {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldCheckIn, map[string]any{"latitude": input.Latitude, "longitude": input.Longitude, "occurred_at": input.OccurredAt}))
}

func (s *Service) SubmitFieldRecord(ctx context.Context, p platform.Principal, itemID string, input domain.FieldRecordInput) error {
	if strings.TrimSpace(input.RawData) == "" || strings.TrimSpace(input.Environment) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldRecordSubmitted, map[string]any{"raw_data": input.RawData, "environment": input.Environment, "evidence_urls": input.EvidenceURLs}))
}

func (s *Service) ReportDeviation(ctx context.Context, p platform.Principal, itemID string, input domain.DeviationInput) (string, error) {
	if strings.TrimSpace(input.Description) == "" {
		return "", ErrValidation
	}
	id := "DV-" + ulid.Make().String()
	return id, s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventDeviationReported, map[string]any{"deviation_id": id, "description": input.Description, "severity": input.Severity, "evidence_url": input.EvidenceURL, "decision": "PENDING"}))
}

func (s *Service) ReviewDeviation(ctx context.Context, p platform.Principal, deviationID string, input domain.DeviationReviewInput) error {
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "RELEASE" && decision != "TERMINATE" && decision != "RETEST" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", "", EventDeviationReviewed, map[string]any{"deviation_id": deviationID, "decision": decision, "comment": input.Comment}))
}

func (s *Service) CompleteFieldImplementation(ctx context.Context, p platform.Principal, projectID string) error {
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventFieldImplementationDone, map[string]any{"confirmed_by": p.UserID}))
}

func (s *Service) ListDeliveryEvents(ctx context.Context, p platform.Principal, projectID string) ([]domain.DeliveryEvent, error) {
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	return repo.ListDeliveryEvents(ctx, p.TenantID, projectID)
}
func (s *Service) UpsertCapability(ctx context.Context, p platform.Principal, item domain.Capability) (domain.Capability, error) {
	if item.ResourceType != "PERSON" && item.ResourceType != "EQUIPMENT" || item.ResourceID == "" || item.ResourceName == "" || len(item.Codes) == 0 {
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
func (s *Service) ListCapabilities(ctx context.Context, p platform.Principal, typ string) ([]domain.Capability, error) {
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	return repo.ListCapabilities(ctx, p.TenantID, typ)
}
func (s *Service) applyEvent(ctx context.Context, event domain.DeliveryEvent) error {
	repo, e := s.deliveryRepo()
	if e != nil {
		return e
	}
	return repo.ApplyDeliveryEvent(ctx, event)
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

package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

type metricWorkflowScene struct {
	repo     *Repository
	filter   platform.ScopeFilter
	item     serviceItemRecord
	resource capabilityRecord
	start    time.Time
}

// newMetricWorkflowScene 用途：在明确隔离库构造一个项目、服务项、资源及可用容量。
// 参数：t 为仅使用PM_METRIC_FACTS_TEST_DSN_FILE的测试，kind 为人员或设备。
// 返回值：真实MySQL仓储和合成场景，不使用现行配置或业务数据。
func newMetricWorkflowScene(t *testing.T, kind string) metricWorkflowScene {
	t.Helper()
	db := metricMySQLDB(t)
	now := time.Now().UTC()
	tenant := ulid.Make().String()
	project := projectRecord{ID: ulid.Make().String(), ContractID: ulid.Make().String(), TenantID: tenant,
		OwnerOrgID: "org-a", CreatedAt: now, UpdatedAt: now}
	item := serviceItemRecord{ID: ulid.Make().String(), TenantID: tenant, ProjectID: project.ID,
		ProjectManagerID: "manager", Status: "实施中", Version: 1, EngineerIDs: []byte(`["person-a"]`),
		EquipmentIDs: []byte("[]"), RequiredCodes: []byte("[]"), CreatedAt: now, UpdatedAt: now}
	resource := capabilityRecord{ID: ulid.Make().String(), TenantID: tenant, ResourceType: kind,
		ResourceID: ulid.Make().String(), ResourceName: "隔离资源", Status: "ACTIVE", IdentityStatus: "ACTIVE",
		CapabilityCodes: []byte("[]"), UpdatedAt: now}
	if kind == "PERSON" {
		resource.UserID = "person-a"
	}
	for _, value := range []any{&project, &item, &resource} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal("隔离场景构造失败：", err)
		}
	}
	s := metricWorkflowScene{repo: NewRepository(db), filter: platform.ScopeFilter{TenantID: tenant, AllowAll: true},
		item: item, resource: resource, start: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)}
	_, err := s.repo.PublishMetricCapacity(t.Context(), s.filter, "capacity-admin", domain.ResourceCapacityInput{
		ResourceType: kind, ResourceID: resource.ResourceID, BusinessDate: "2026-11-01", OrganizationID: "org-a",
		OrganizationWitnessID: "directory-witness", Intervals: []domain.MetricInterval{{Start: s.start, End: s.start.Add(4 * time.Hour)}},
		Note: "隔离明确容量", IdempotencyKey: "capacity-" + tenant,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestMetricItemResourcesMinimumOnMySQL 用途：验证当前服务项读取不受全局第一页影响且保留SELF及租户范围。
// 参数：t 为显式隔离MySQL场景。
// 返回值：无；混入其他服务项、其他人或其他租户记录时失败。
func TestMetricItemResourcesMinimumOnMySQL(t *testing.T) {
	s := newMetricWorkflowScene(t, "PERSON")
	ctx := context.Background()
	use := domain.ResourceUsage{ID: ulid.Make().String(), TenantID: s.filter.TenantID, ResourceType: "PERSON",
		ResourceID: s.resource.ResourceID, IdentityID: "person-a", ProjectID: s.item.ProjectID,
		ServiceItemID: s.item.ID, BusinessDate: "2026-11-01", OrganizationID: "org-a",
		Start: s.start, End: s.start.Add(time.Hour), Minutes: 60, Status: "DRAFT", Version: 1,
		CreatedBy: "person-a", CreatedAt: s.start, UpdatedAt: s.start}
	rows := []metricUsageRecord{{ResourceUsage: use}}
	other := use
	other.ID, other.IdentityID, other.ResourceID, other.CreatedBy = ulid.Make().String(), "person-b", "person-b-resource", "person-b"
	rows = append(rows, metricUsageRecord{other})
	for index := 0; index < 55; index++ {
		outside := use
		outside.ID, outside.ServiceItemID, outside.CreatedAt = ulid.Make().String(), "other-item", s.start.Add(time.Hour)
		rows = append(rows, metricUsageRecord{outside})
	}
	foreign := use
	foreign.ID, foreign.TenantID = ulid.Make().String(), "another-tenant"
	rows = append(rows, metricUsageRecord{foreign})
	if err := s.repo.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	global, err := s.repo.ListMetricUsages(ctx, s.filter, 1)
	if err != nil || len(global) != 50 {
		t.Fatalf("全局分页夹具不成立：%d %v", len(global), err)
	}
	self := platform.ScopeFilter{TenantID: s.filter.TenantID, UserID: "person-a", AllowSelf: true}
	actual, err := s.repo.ListItemMetricUsages(ctx, self, s.item.ID, 1)
	if err != nil || len(actual) != 1 || actual[0].ID != use.ID {
		t.Fatalf("父项或资源范围异常：%+v %v", actual, err)
	}
	caps, err := s.repo.ListItemMetricCapacities(ctx, self, domain.ServiceItem{ID: s.item.ID}, 1)
	if err != nil || len(caps) != 1 || caps[0].ResourceID != s.resource.ResourceID {
		t.Fatalf("关联容量异常：%+v %v", caps, err)
	}
	for _, row := range global {
		if row.ID == use.ID {
			t.Fatal("夹具必须把目标行排在全局第一页之外")
		}
	}
	page2, err := s.repo.ListItemMetricUsages(ctx, self, s.item.ID, 2)
	if err != nil || len(page2) != 0 {
		t.Fatalf("父项分页异常：%+v %v", page2, err)
	}
}

// TestMetricDeviceWorkflowMinimumOnMySQL 用途：验证准备设备的实际登记、独立确认、节点回读及统计复用。
// 参数：t 为仅包含一台设备及一个服务项的隔离场景。
// 返回值：无；时长、状态、独立确认或父项边界错误时失败。
func TestMetricDeviceWorkflowMinimumOnMySQL(t *testing.T) {
	s := newMetricWorkflowScene(t, "EQUIPMENT")
	service := &application.Service{Repo: s.repo}
	p := platform.Principal{TenantID: s.filter.TenantID, UserID: "manager",
		Permissions: map[string]bool{"project.read": true, "project.device.manage": true,
			"project.resource_usage.record": true, "project.resource_usage.confirm": true},
		DataScopes: []platform.DataScope{{RoleCode: "device_admin", ScopeType: "APPLICATION"}}}
	use, err := service.CreateMetricUsage(t.Context(), p, domain.ResourceUsageInput{
		ResourceType: "EQUIPMENT", ResourceID: s.resource.ResourceID, ServiceItemID: s.item.ID,
		BusinessDate: "2026-11-01", Start: s.start, End: s.start.Add(time.Hour),
		Note: "现场设备运行", Evidence: "合成现场运行记录", IdempotencyKey: "device-use",
	})
	if err != nil || use.Status != "DRAFT" || use.Minutes != 60 {
		t.Fatalf("设备实际登记异常：%+v %v", use, err)
	}
	use, err = service.MutateMetricUsage(t.Context(), p, use.ID, "SUBMIT", domain.ResourceUsageDecision{ExpectedVersion: 1, IdempotencyKey: "device-submit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.MutateMetricUsage(t.Context(), p, use.ID, "DECISION", domain.ResourceUsageDecision{
		ExpectedVersion: use.Version, Decision: "CONFIRMED", Note: "禁止自审", IdempotencyKey: "device-self"}); err == nil {
		t.Fatal("设备登记人不应确认自己的记录")
	}
	p.UserID = "independent-reviewer"
	use, err = service.MutateMetricUsage(t.Context(), p, use.ID, "DECISION", domain.ResourceUsageDecision{
		ExpectedVersion: use.Version, Decision: "CONFIRMED", Note: "独立核对实际运行", IdempotencyKey: "device-confirm"})
	if err != nil || use.Status != "CONFIRMED" {
		t.Fatalf("独立确认异常：%+v %v", use, err)
	}
	result, err := service.MetricItemResources(t.Context(), p, s.item.ID, "usages", 1)
	if err != nil {
		t.Fatal(err)
	}
	actual := result.([]domain.ResourceUsage)
	if len(actual) != 1 || actual[0].ID != use.ID || actual[0].Minutes != 60 {
		t.Fatalf("节点回读异常：%+v", actual)
	}
	snapshot, err := s.repo.ReadMetricFacts(t.Context(), s.filter.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range snapshot.Rows {
		if row.Kind == "equipment_utilization" && row.CapacityMinutes == 240 && row.ActualMinutes == 60 {
			found = true
		}
	}
	if !found {
		t.Fatalf("设备统计未复用真实台账：%+v", snapshot.Rows)
	}
	var item serviceItemRecord
	if err = s.repo.db.First(&item, "id=?", s.item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.Status != s.item.Status || item.Version != s.item.Version {
		t.Fatal("用量登记错误推进父服务项")
	}
}

// TestMetricPersonCandidateMinimumOnMySQL 用途：验证现场证据生成候选、精确登记重放且不伪造确认。
// 参数：t 为一个人员、一个服务项和一组不可变合成现场证据的隔离场景。
// 返回值：无；候选时段错误、重复登记或默认自动确认时失败。
func TestMetricPersonCandidateMinimumOnMySQL(t *testing.T) {
	s := newMetricWorkflowScene(t, "PERSON")
	for index, kind := range []string{application.EventFieldCheckedIn, application.EventFieldRecordSubmitted, application.EventFieldSignatureCaptured} {
		at := s.start
		if index > 0 {
			at = at.Add(time.Hour)
		}
		event := deliveryEventRecord{ID: ulid.Make().String(), TenantID: s.filter.TenantID, ProjectID: s.item.ProjectID,
			ServiceItemID: s.item.ID, EventType: kind, ActorUserID: "person-a", Payload: []byte("{}"), CreatedAt: at, CapturedAt: &at}
		if err := s.repo.db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := &application.Service{Repo: s.repo}
	p := platform.Principal{TenantID: s.filter.TenantID, UserID: "person-a",
		Permissions: map[string]bool{"project.read": true, "project.field.execute": true, "project.resource_usage.record": true},
		DataScopes:  []platform.DataScope{{RoleCode: "engineer", ScopeType: "APPLICATION"}}}
	candidates, err := service.MetricUsageCandidates(t.Context(), p, s.item.ID)
	if err != nil || len(candidates) != 1 || !candidates[0].Complete || candidates[0].Minutes != 60 {
		t.Fatalf("真实候选异常：%+v %v", candidates, err)
	}
	input := domain.MetricUsageCandidateApplyInput{ResourceID: s.resource.ResourceID, BusinessDate: "2026-11-01", IdempotencyKey: "person-candidate"}
	first, err := service.ApplyMetricUsageCandidate(t.Context(), p, s.item.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	later := s.start.Add(90 * time.Minute)
	event := deliveryEventRecord{ID: ulid.Make().String(), TenantID: s.filter.TenantID, ProjectID: s.item.ProjectID,
		ServiceItemID: s.item.ID, EventType: application.EventFieldRecordSubmitted, ActorUserID: "person-a",
		Payload: []byte("{}"), CreatedAt: later, CapturedAt: &later}
	if err = s.repo.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	replay, err := service.ApplyMetricUsageCandidate(t.Context(), p, s.item.ID, input)
	if err != nil || replay.ID != first.ID || first.Status != "DRAFT" || first.AutoConfirmed || first.Source != "FIELD_EVENTS" {
		t.Fatalf("候选重放或确认语义错误：%+v %+v %v", first, replay, err)
	}
	if replay.Minutes != 60 || !replay.End.Equal(first.End) {
		t.Fatal("增加现场事件后不得改变原命令回执时段")
	}
	otherActor := p
	otherActor.UserID = "other-person"
	if _, err = service.ApplyMetricUsageCandidate(t.Context(), otherActor, s.item.ID, input); err == nil {
		t.Fatal("其他操作者不能取回原候选命令")
	}
	withoutPermission := p
	withoutPermission.Permissions = map[string]bool{"project.read": true, "project.field.execute": true}
	if _, err = service.ApplyMetricUsageCandidate(t.Context(), withoutPermission, s.item.ID, input); err == nil {
		t.Fatal("撤销登记权限后不能重放原候选命令")
	}
	changedInput := input
	changedInput.BusinessDate = "2026-11-02"
	if _, err = service.ApplyMetricUsageCandidate(t.Context(), p, s.item.ID, changedInput); err == nil {
		t.Fatal("同一操作号不能改绑其他业务日")
	}
	foreignTenant := p
	foreignTenant.TenantID = "another-tenant"
	if _, err = service.ApplyMetricUsageCandidate(t.Context(), foreignTenant, s.item.ID, input); err == nil {
		t.Fatal("其他租户不能取回原候选命令")
	}
	var count int64
	if err = s.repo.db.Model(&metricUsageRecord{}).Where("tenant_id=?", s.filter.TenantID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("候选重放重复写入：%d %v", count, err)
	}
}

// TestMetricHiddenCapacityAssociationMinimumOnMySQL 用途：验证不可见用量不能揭示设备与服务项的关系。
// 参数：t 为同设备跨日期换组织的显式隔离数据库场景。
// 返回值：无；A组织因B组织私有用量看到未列入计划的设备容量时失败。
func TestMetricHiddenCapacityAssociationMinimumOnMySQL(t *testing.T) {
	s := newMetricWorkflowScene(t, "EQUIPMENT")
	day2 := s.start.AddDate(0, 0, 1)
	capacity, err := s.repo.PublishMetricCapacity(t.Context(), s.filter, "admin", domain.ResourceCapacityInput{
		ResourceType: "EQUIPMENT", ResourceID: s.resource.ResourceID, BusinessDate: "2026-11-02",
		OrganizationID: "org-b", OrganizationWitnessID: "witness-b",
		Intervals: []domain.MetricInterval{{Start: day2, End: day2.Add(time.Hour)}}, Note: "B组织明确容量", IdempotencyKey: "capacity-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	usage := metricUsageRecord{ResourceUsage: domain.ResourceUsage{ID: ulid.Make().String(), TenantID: s.filter.TenantID,
		ResourceType: "EQUIPMENT", ResourceID: s.resource.ResourceID, ProjectID: s.item.ProjectID,
		ServiceItemID: s.item.ID, BusinessDate: "2026-11-02", OrganizationID: "org-b", CapacityID: capacity.ID,
		Start: day2, End: day2.Add(time.Hour), Minutes: 60, Status: "DRAFT", Version: 1,
		CreatedBy: "private-operator", CreatedAt: day2, UpdatedAt: day2}}
	if err = s.repo.db.Create(&usage).Error; err != nil {
		t.Fatal(err)
	}
	reader := platform.ScopeFilter{TenantID: s.filter.TenantID, UserID: "org-reader", OrganizationIDs: []string{"org-a"}}
	uses, err := s.repo.ListItemMetricUsages(t.Context(), reader, s.item.ID, 1)
	if err != nil || len(uses) != 0 {
		t.Fatalf("私有用量夹具不成立：%+v %v", uses, err)
	}
	caps, err := s.repo.ListItemMetricCapacities(t.Context(), reader, domain.ServiceItem{ID: s.item.ID}, 1)
	if err != nil || len(caps) != 0 {
		t.Fatalf("隐藏用量泄露设备关联：%+v %v", caps, err)
	}
	if err = s.repo.db.Model(&usage).Update("created_by", "org-reader").Error; err != nil {
		t.Fatal(err)
	}
	caps, err = s.repo.ListItemMetricCapacities(t.Context(), reader, domain.ServiceItem{ID: s.item.ID}, 1)
	if err != nil || len(caps) != 1 || caps[0].OrganizationID != "org-a" {
		t.Fatalf("合法代登关联未按原范围显示：%+v %v", caps, err)
	}
}

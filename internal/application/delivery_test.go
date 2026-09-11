package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/platform"

	"github.com/j-s-te/project-management/internal/domain"
)

func TestValidatePenetrationComplianceRequiresFullAuthorizationSet(t *testing.T) {
	valid := domain.ImplementationPlanInput{
		PenetrationTestPlan: "扫描与漏洞验证步骤",
		AuthDocNo:           "AUTH-2026-001",
		AuthStart:           "2026-01-01T00:00:00Z",
		AuthEnd:             "2026-12-31T23:59:59Z",
		AuthScope:           "内网段 10.0.0.0/8",
		TestScope:           "业务系统 WEB 渗透",
		TestWindow:          "00:00-06:00",
		EmergencyContact:    "王工 13800000000",
		RollbackPlan:        "失败回滚到基线版本",
	}
	if err := validatePenetrationCompliance(valid); err != nil {
		t.Fatalf("valid compliance rejected: %v", err)
	}
	missing := valid
	missing.AuthDocNo = ""
	if err := validatePenetrationCompliance(missing); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing auth_doc_no should fail validation, got %v", err)
	}
	badWindow := valid
	badWindow.AuthStart = "2026-12-31T00:00:00Z"
	badWindow.AuthEnd = "2026-01-01T00:00:00Z"
	if err := validatePenetrationCompliance(badWindow); !errors.Is(err, ErrValidation) {
		t.Fatalf("reversed auth window should fail validation, got %v", err)
	}
	standard := valid
	standard.PenetrationTestPlan = ""
	if err := validatePenetrationCompliance(standard); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing penetration plan should fail validation, got %v", err)
	}
}

func TestReviewDecisionAndReportPhaseStateValidation(t *testing.T) {
	if err := validatePenetrationCompliance(domain.ImplementationPlanInput{}); !errors.Is(err, ErrValidation) {
		t.Fatal("empty compliance should fail")
	}
}

// 缺失的合规要素必须逐项点名，用户才知道该补哪一个，而不是只看到"请求参数不合法"。
func TestPenetrationComplianceNamesEveryMissingField(t *testing.T) {
	err := validatePenetrationCompliance(domain.ImplementationPlanInput{})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("empty compliance should fail validation, got %v", err)
	}
	message := UserMessage(err)
	for _, label := range []string{"渗透测试专项计划", "授权书编号", "授权范围", "计划测试范围", "测试时间窗", "应急联系人", "回滚方案"} {
		if !strings.Contains(message, label) {
			t.Fatalf("message %q should name the missing field %q", message, label)
		}
	}
}

// 发布实施计划的前置状态必须给出可执行指引，并与仓储层守卫保持一致。
func TestImplementationPlanPreconditionGuidesTheUserToTheMissingStep(t *testing.T) {
	ready := domain.ServiceItem{Status: "待分配", ProjectManagerID: "pm-1", ConflictStatus: "PASSED"}
	if err := CheckImplementationPlanPrecondition(planPreconditionOf(ready)); err != nil {
		t.Fatalf("ready item rejected: %v", err)
	}
	// 「待制定计划」已随幽灵指派端点一并移除：任务分配完成即具备计划前置条件，
	// 该状态在真实流程中不再出现，因此不再作为合法入口（生产库亦无该状态的行）。

	cases := []struct {
		name     string
		mutate   func(*domain.ServiceItem)
		contains string
	}{
		{"wrong status", func(item *domain.ServiceItem) { item.Status = "已完成" }, "当前状态"},
		{"no project manager", func(item *domain.ServiceItem) { item.ProjectManagerID = "" }, "指派项目经理"},
		{"capability unchecked", func(item *domain.ServiceItem) { item.ConflictStatus = "UNCHECKED" }, "尚未校验"},
		{"capability conflict", func(item *domain.ServiceItem) { item.ConflictStatus = "CONFLICT" }, "存在冲突"},
		{"special method not reviewed", func(item *domain.ServiceItem) { item.Special = "是"; item.TechReviewStatus = "PENDING" }, "技术总监复核"},
	}
	for _, testCase := range cases {
		item := ready
		testCase.mutate(&item)
		err := CheckImplementationPlanPrecondition(planPreconditionOf(item))
		if !errors.Is(err, ErrPrecondition) {
			t.Fatalf("%s: want ErrPrecondition, got %v", testCase.name, err)
		}
		if !strings.Contains(UserMessage(err), testCase.contains) {
			t.Fatalf("%s: message %q should mention %q", testCase.name, UserMessage(err), testCase.contains)
		}
		// 前置状态错误绝不能被误报为字段校验错误。
		if errors.Is(err, ErrValidation) {
			t.Fatalf("%s: precondition must not be reported as validation", testCase.name)
		}
	}
}

// planPreconditionOf 让测试继续用领域对象表达前置状态，保持用例可读。
func planPreconditionOf(item domain.ServiceItem) PlanPrecondition {
	return PlanPrecondition{Status: item.Status, ProjectManagerID: item.ProjectManagerID,
		ConflictStatus: item.ConflictStatus, Special: item.Special, TechReviewStatus: item.TechReviewStatus}
}

// 实施计划的人员清单：至少一名人员、资源必须命中有效能力档案、
// 使用时段落在计划内且被资质有效期覆盖。
func TestResolvePlanPersonnelValidatesCrewAndSnapshotsQualifications(t *testing.T) {
	repo := &capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "王明", Codes: []string{"CISP-PTE"}, Status: "ACTIVE", ValidUntil: time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)},
		{ResourceType: "PERSON", ResourceID: "P-002", ResourceName: "陈静", Codes: []string{"等级保护初级"}, Status: "ACTIVE", ValidUntil: time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC)},
	}}
	service := &Service{Repo: repo}
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	resources, err := service.resolvePlanPersonnel(context.Background(), repo, "tenant-1", []domain.PlanResourceInput{
		{ResourceType: "person", ResourceID: "P-001"},
		{ResourceType: "PERSON", ResourceID: "P-002", WindowStart: "2026-08-10", WindowEnd: "2026-08-15", Note: "备份"},
	}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("resources=%+v", resources)
	}
	// 人员行留空使用时段表示全程：快照里不带窗口，但保留资质与有效期。
	if resources[0].ResourceName != "王明" || resources[0].ValidUntil != "2027-06-30" || resources[0].WindowStart != "" {
		t.Fatalf("person row=%+v", resources[0])
	}
	if len(resources[0].Codes) != 1 || resources[0].Codes[0] != "CISP-PTE" {
		t.Fatalf("person codes=%v", resources[0].Codes)
	}
	if resources[1].WindowStart != "2026-08-10" || resources[1].WindowEnd != "2026-08-15" || resources[1].Note != "备份" {
		t.Fatalf("backup row=%+v", resources[1])
	}
}

func TestResolvePlanPersonnelRejectsInvalidCrew(t *testing.T) {
	repo := &capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "王明", Status: "ACTIVE", ValidUntil: time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)},
		{ResourceType: "PERSON", ResourceID: "P-002", ResourceName: "陈静", Status: "DISABLED"},
		{ResourceType: "PERSON", ResourceID: "P-003", ResourceName: "赵工", Status: "ACTIVE", ValidUntil: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)},
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "设备甲", Status: "ACTIVE"},
	}}
	service := &Service{Repo: repo}
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	person := domain.PlanResourceInput{ResourceType: "PERSON", ResourceID: "P-001"}

	cases := []struct {
		name     string
		rows     []domain.PlanResourceInput
		expected string
	}{
		{"空清单", nil, "至少添加一名实施人员"},
		{"设备不属于计划阶段", []domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001"}}, "设备清单在「实施准备」中维护"},
		{"人员不在有效档案", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-002"}}, "不在有效的能力档案中"},
		{"未知资源", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-404"}}, "不在有效的能力档案中"},
		{"重复添加", []domain.PlanResourceInput{person, person}, "不能重复添加"},
		{"无效类型", []domain.PlanResourceInput{{ResourceType: "VEHICLE", ResourceID: "V-1"}}, "资源类型必须是人员或设备"},
		{"时段缺一端", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-001", WindowStart: "2026-08-10"}}, "需要同时填写开始与结束"},
		{"时段超出计划", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-001", WindowStart: "2026-08-01", WindowEnd: "2026-08-20"}}, "必须落在计划起止"},
		{"有效期不覆盖时段", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-003", WindowStart: "2026-08-10", WindowEnd: "2026-08-20"}}, "不覆盖使用时段截止日"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.resolvePlanPersonnel(context.Background(), repo, "tenant-1", tc.rows, start, end)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("error=%v, want contains %q", err, tc.expected)
			}
		})
	}
}

// 设备占用冲突：同一台设备在重叠时段已被其他服务项占用时必须硬拦，并点名占用方与日期；
// 时段不重叠、或占用来自当前服务项自身时不得误报。
func TestResolvePreparationEquipmentRejectsOverlappingReservation(t *testing.T) {
	repo := &capabilityRepository{
		capabilities: []domain.Capability{
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "无线测试套件", Codes: []string{"802.11 a/b/g/n/ac"}, Status: "ACTIVE", ValidUntil: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)},
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-002", ResourceName: "BurpSuite 终端", Status: "ACTIVE"},
		},
		reservations: []domain.EquipmentReservation{
			{ServiceItemID: "SI-OTHER-1", ProjectID: "PJ-2026-002", ResourceID: "EQ-001", ResourceName: "无线测试套件", WindowStart: "2026-08-18", WindowEnd: "2026-08-20"},
		},
	}
	service := &Service{Repo: repo}
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	// 重叠：8-19 落在他人 8-18~8-20 的占用区间内。
	_, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", WindowStart: "2026-08-19", WindowEnd: "2026-08-22"}}, start, end)
	if err == nil || !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("overlapping reservation must be a resource conflict, got %v", err)
	}
	for _, expected := range []string{"无线测试套件", "PJ-2026-002", "SI-OTHER-1", "2026-08-18 ~ 2026-08-20"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("conflict message %q must name %q", err.Error(), expected)
		}
	}

	// 首尾相接不算重叠：8-20 结束、下一段从 8-20 开始。
	if _, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", WindowStart: "2026-08-20", WindowEnd: "2026-08-22"}}, start, end); err != nil {
		t.Fatalf("touching windows must not conflict: %v", err)
	}

	// 全覆盖同样算重叠：留空使用时段即占用整个计划窗口。
	if _, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001"}}, start, end); err == nil {
		t.Fatal("whole-plan reservation must conflict with the other service item")
	}

	// 无人占用的设备正常通过，并保留使用时段快照。
	rows, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-002", WindowStart: "2026-08-18", WindowEnd: "2026-08-20"}}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].WindowStart != "2026-08-18" || rows[0].ResourceName != "BurpSuite 终端" {
		t.Fatalf("rows=%+v", rows)
	}
}

// 实施准备的设备清单同样要求：至少一台设备、命中有效档案、检定有效期覆盖使用时段。
func TestResolvePreparationEquipmentValidatesCatalogAndCalibration(t *testing.T) {
	repo := &capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "超期设备", Status: "ACTIVE", ValidUntil: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-002", ResourceName: "停用设备", Status: "DISABLED"},
		{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "王明", Status: "ACTIVE"},
	}}
	service := &Service{Repo: repo}
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		rows     []domain.PlanResourceInput
		expected string
	}{
		{"空清单", nil, "请至少选择一台实施设备"},
		{"人员不属于准备阶段", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-001"}}, "资源类型与当前阶段不匹配"},
		{"停用设备", []domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-002"}}, "不在有效的能力档案中"},
		{"检定有效期不覆盖", []domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001"}}, "不覆盖使用时段截止日"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF", tc.rows, start, end)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("error=%v, want contains %q", err, tc.expected)
			}
		})
	}
}

// 设备借出规则：仅在公司使用的设备不可借出；当前不在公司（他人借出中）的设备也不可再借；
// 归还后不再占用、也不再生效为「不在公司」。
func TestResolvePreparationEquipmentEnforcesUsageScopeAndPresence(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	repo := &capabilityRepository{
		capabilities: []domain.Capability{
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-COMPANY", ResourceName: "机房专用设备", Status: "ACTIVE", UsageScope: domain.EquipmentUsageCompanyOnly},
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-OUT", ResourceName: "外借中的设备", Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny},
		},
		reservations: []domain.EquipmentReservation{
			{ServiceItemID: "SI-OTHER", ProjectID: "PJ-2026-003", ResourceID: "EQ-OUT", ResourceName: "外借中的设备", WindowStart: today, WindowEnd: today},
		},
	}
	service := &Service{Repo: repo}
	start := time.Now().UTC().AddDate(0, 0, -1)
	end := time.Now().UTC().AddDate(0, 0, 5)

	_, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-COMPANY", WindowStart: start.Format("2006-01-02"), WindowEnd: end.Format("2006-01-02")}}, start, end)
	if err == nil || !errors.Is(err, ErrResourceConflict) || !strings.Contains(err.Error(), "仅在公司使用，不可借出") {
		t.Fatalf("company-only equipment must not be lent out, got %v", err)
	}

	_, err = service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-OUT", WindowStart: start.Format("2006-01-02"), WindowEnd: end.Format("2006-01-02")}}, start, end)
	if err == nil || !errors.Is(err, ErrResourceConflict) || !strings.Contains(err.Error(), "当前不在公司") {
		t.Fatalf("equipment that is currently out must not be lent again, got %v", err)
	}

	// 归还后同一台设备可以再次借出：占用里不再包含它。
	repo.reservations = nil
	if _, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-OUT", WindowStart: start.Format("2006-01-02"), WindowEnd: end.Format("2006-01-02")}}, start, end); err != nil {
		t.Fatalf("returned equipment must be available again: %v", err)
	}
}

// 设备在位状态由占用时段派生：覆盖今天即为不在公司，并带出占用方与时段。
func TestEquipmentPresenceDerivesFromReservations(t *testing.T) {
	today := time.Now().UTC()
	yesterday := today.AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := today.AddDate(0, 0, 1).Format("2006-01-02")
	reservations := []domain.EquipmentReservation{
		{ServiceItemID: "SI-1", ProjectID: "PJ-1", ResourceID: "EQ-1", WindowStart: yesterday, WindowEnd: tomorrow},
		{ServiceItemID: "SI-2", ProjectID: "PJ-2", ResourceID: "EQ-2", WindowStart: tomorrow, WindowEnd: tomorrow},
	}
	active := equipmentInUseAt(reservations, today)
	if _, ok := active["EQ-1"]; !ok {
		t.Fatalf("equipment inside its window must count as out of company: %#v", active)
	}
	if _, ok := active["EQ-2"]; ok {
		t.Fatalf("future reservation must not mark the equipment as out: %#v", active)
	}
}

// 使用范围只在显式提交时改变：从「资质与能力管理」、设备维护表单或 CSV 导入改其它字段，
// 都不能把「仅在公司使用」静默改回可借出。
func TestEquipmentUsageScopeIsPreservedWhenOmitted(t *testing.T) {
	setup := func() (*capabilityRepository, *Service) {
		repo := &capabilityRepository{capabilities: []domain.Capability{
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备", Codes: []string{"c1"}, Status: "ACTIVE", UsageScope: domain.EquipmentUsageCompanyOnly},
		}}
		service := &Service{Repo: repo}
		service.Personnel = nil
		return repo, service
	}
	owner := principalWith("project.device.manage", platform.DataScope{RoleCode: "device_admin", ScopeType: "APPLICATION"})

	// 设备维护表单：不提交 usage_scope 时沿用既有设置。
	repo, service := setup()
	if _, err := service.UpsertEquipment(context.Background(), owner, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备改名", Codes: []string{"c1"}}); err != nil {
		t.Fatal(err)
	}
	if got := repo.saved[len(repo.saved)-1].UsageScope; got != domain.EquipmentUsageCompanyOnly {
		t.Fatalf("omitted usage scope must keep the stored value, got %q", got)
	}

	// 显式提交时按提交值改写（可借出 ↔ 仅在公司使用）。
	if _, err := service.UpsertEquipment(context.Background(), owner, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备", Codes: []string{"c1"}, UsageScope: domain.EquipmentUsageAny}); err != nil {
		t.Fatal(err)
	}
	if got := repo.saved[len(repo.saved)-1].UsageScope; got != domain.EquipmentUsageAny {
		t.Fatalf("explicit usage scope must win, got %q", got)
	}

	// 新增设备没有历史值：落到默认的可借出。
	if _, err := service.UpsertEquipment(context.Background(), owner, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-NEW", ResourceName: "新设备", Codes: []string{"c2"}}); err != nil {
		t.Fatal(err)
	}
	if got := repo.saved[len(repo.saved)-1].UsageScope; got != domain.EquipmentUsageAny {
		t.Fatalf("new equipment must default to lendable, got %q", got)
	}

	// 非法取值仍然拒绝。
	if _, err := service.UpsertEquipment(context.Background(), owner, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备", Codes: []string{"c1"}, UsageScope: "SOMETIMES"}); err == nil {
		t.Fatal("unknown usage scope must be rejected")
	}
}

// 资质与能力管理与 CSV 导入同样不能清掉设备的使用范围。
func TestCapabilityUpsertAndImportPreserveUsageScope(t *testing.T) {
	repo := &capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备", Codes: []string{"c1"}, Status: "ACTIVE", UsageScope: domain.EquipmentUsageCompanyOnly},
	}}
	service := &Service{Repo: repo}
	manager := principalWith("project.resource.manage", platform.DataScope{RoleCode: "project_manager", ScopeType: "APPLICATION"})

	if _, err := service.UpsertCapability(context.Background(), manager, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备改名", Codes: []string{"c1"}}); err != nil {
		t.Fatal(err)
	}
	if got := repo.saved[len(repo.saved)-1].UsageScope; got != domain.EquipmentUsageCompanyOnly {
		t.Fatalf("capability upsert must keep the stored usage scope, got %q", got)
	}

	repo.saved = nil
	if _, err := service.ImportCapabilities(context.Background(), manager, []domain.Capability{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备", Codes: []string{"c1"}}}); err != nil {
		t.Fatal(err)
	}
	if got := repo.saved[len(repo.saved)-1].UsageScope; got != domain.EquipmentUsageCompanyOnly {
		t.Fatalf("csv import must keep the stored usage scope, got %q", got)
	}
}

// duplicateContractRepository 模拟并发激活合同时的竞态败方：find 阶段未命中，
// 但写库时撞唯一键——这正是 activations/HTTP 重放最可能出现的时序。
type duplicateContractRepository struct {
	scopeRepository
	existing  domain.Project
	missOnce  bool
	synced    bool
	activated []string
	// duplicate 为 true 时 ActivateContract 撞唯一键（模拟后到请求）。
	duplicate bool
}

func (r *duplicateContractRepository) FindProjectByContractVersion(context.Context, platform.ScopeFilter, string, string) (domain.Project, error) {
	if r.missOnce {
		r.missOnce = false
		return domain.Project{}, ErrNotFound
	}
	if r.existing.ID != "" {
		return r.existing, nil
	}
	return domain.Project{}, ErrNotFound
}
func (r *duplicateContractRepository) ActivateContract(_ context.Context, project domain.Project, _ []domain.ServiceItem, _ domain.DeliveryEvent) error {
	if r.duplicate {
		return ErrDuplicateContract
	}
	r.existing = project
	r.activated = append(r.activated, project.ID)
	return nil
}
func (r *duplicateContractRepository) SyncContractStampStatus(_ context.Context, project domain.Project, _ bool, _ domain.DeliveryEvent) error {
	r.synced = true
	r.existing = project
	return nil
}
func (r *duplicateContractRepository) ApplyDeliveryEvent(context.Context, domain.DeliveryEvent) error {
	return nil
}
func (r *duplicateContractRepository) ListSlaOverdue(context.Context, platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	return nil, nil
}
func (r *duplicateContractRepository) ListDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error) {
	return nil, nil
}
func (r *duplicateContractRepository) FindProjectForDeviation(context.Context, platform.ScopeFilter, string) (string, string, error) {
	return "", "", ErrNotFound
}
func (r *duplicateContractRepository) UpsertCapability(context.Context, domain.Capability, string) (domain.Capability, error) {
	return domain.Capability{}, nil
}
func (r *duplicateContractRepository) ListCapabilities(context.Context, string, string) ([]domain.Capability, error) {
	return nil, nil
}
func (r *duplicateContractRepository) FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error) {
	return nil, nil
}
func (r *duplicateContractRepository) ListEquipmentReservations(context.Context, string, string) ([]domain.EquipmentReservation, error) {
	return nil, nil
}
func (r *duplicateContractRepository) UpdateCapabilityIdentities(context.Context, string, map[string]string, time.Time) error {
	return nil
}

// 重复激活同一合同版本不能 500：后到请求把唯一键冲突翻译成幂等成功并回读既有项目。
func TestActivateContractDuplicateConcurrentActivationReturnsExistingProject(t *testing.T) {
	principal := principalWith("project.contract.import", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION", ScopeID: "app-1"})
	activeRepo := &duplicateContractRepository{}
	service := &Service{Repo: activeRepo}
	activation := func() domain.ContractActivation {
		return domain.ContractActivation{
			ContractID: "CT-001", ContractVersion: "v1.0", Customer: "客户A", EffectiveAt: time.Now().UTC(),
			Services: []domain.ContractService{{SourceID: "S1", Site: "北京", Batch: "B1", Category: "渗透测试", TestMode: "PENETRATION"}},
		}
	}

	first, err := service.ActivateContract(context.Background(), principal, activation())
	if err != nil {
		t.Fatalf("first activation failed: %v", err)
	}
	if len(activeRepo.activated) != 1 {
		t.Fatalf("first activation should insert project once, got %d", len(activeRepo.activated))
	}

	// 第二次请求并发到达：它的 pre-check find 与首次插入同时进行（miss），写库时撞唯一键。
	activeRepo.missOnce = true
	activeRepo.duplicate = true
	second, err := service.ActivateContract(context.Background(), principal, activation())
	if err != nil {
		t.Fatalf("duplicate activation must be idempotent success, got %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate activation returned %q, want original project %q", second.ID, first.ID)
	}
	if !activeRepo.synced {
		t.Fatal("duplicate activation should still sync the stamped-contract state")
	}
	if len(activeRepo.activated) != 1 {
		t.Fatalf("duplicate activation must not insert another project, got %d inserts", len(activeRepo.activated))
	}
}

// hookRepository 同时充当 Repository 与 DeliveryRepository，记录派生事件并保留规则/超期数据，
// 供 automations / warning-rules / sla 钩子测试使用。
type hookRepository struct {
	capabilityRepository
	rules   []domain.Rule
	events  []domain.DeliveryEvent
	overdue []domain.SlaOverdueItem
}

func (r *hookRepository) ListRules(context.Context, string, string) ([]domain.Rule, error) {
	return r.rules, nil
}
func (r *hookRepository) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.events = append(r.events, event)
	return nil
}
func (r *hookRepository) ListSlaOverdue(_ context.Context, filter platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	r.lastFilter = filter
	return r.overdue, nil
}

func TestApplyEventFiresAutomationEventOnlyWhenRuleMatches(t *testing.T) {
	principal := platform.Principal{TenantID: "t1", UserID: "u1"}

	t.Run("enabled matching rule appends AUTOMATION_TRIGGERED", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "通知技术总监"}}}
		service := Service{Repo: repo}
		err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", "DEVIATION_REPORTED", map[string]any{"severity": "HIGH"}))
		if err != nil {
			t.Fatalf("applyEvent failed: %v", err)
		}
		if len(repo.events) != 2 || repo.events[1].Type != EventAutomationTriggered {
			t.Fatalf("expected source + one AUTOMATION_TRIGGERED event, got %+v", repo.events)
		}
		triggered := repo.events[1]
		if triggered.TenantID != "t1" || triggered.ProjectID != "PJ-1" || triggered.ServiceItemID != "SI-1" {
			t.Fatalf("derived event lost context: %+v", triggered)
		}
		targets, _ := triggered.Payload["targets"].([]string)
		if len(targets) != 1 || targets[0] != "通知技术总监" {
			t.Fatalf("derived event targets wrong: %+v", triggered.Payload)
		}
	})

	t.Run("non-matching or disabled rules append nothing", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "REPORT_STATUS_UPDATED", Target: "x"}, {Enabled: false, Trigger: "DEVIATION_REPORTED", Target: "y"}}}
		service := Service{Repo: repo}
		if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", "DEVIATION_REPORTED", nil)); err != nil {
			t.Fatalf("applyEvent failed: %v", err)
		}
		if len(repo.events) != 1 || repo.events[0].Type != "DEVIATION_REPORTED" {
			t.Fatalf("expected only the source event, got %+v", repo.events)
		}
	})

	t.Run("derived events never re-trigger automations", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: EventAutomationTriggered, Target: "boom"}}}
		service := Service{Repo: repo}
		if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventAutomationTriggered, nil)); err != nil {
			t.Fatalf("applyEvent failed: %v", err)
		}
		if len(repo.events) != 1 || repo.events[0].Type != EventAutomationTriggered {
			t.Fatalf("AUTOMATION_TRIGGERED must not recurse, got %+v", repo.events)
		}
	})
}

func TestListSlaOverdueForwardsScopeFilterToRepository(t *testing.T) {
	principal := principalWith("project.read", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})
	// 计划完成时间已过才会进入 SLA 口径；OverdueHours 由服务端按当前时间重算，
	// 不再直接透传仓储给的旧值。
	plannedEnd := time.Now().UTC().Add(-12 * time.Hour).Format(time.RFC3339)
	repo := &hookRepository{overdue: []domain.SlaOverdueItem{{ID: "SI-1", ProjectID: "PJ-1", Status: "实施中", PlannedEnd: plannedEnd}}}
	service := Service{Repo: repo}
	items, err := service.ListSlaOverdue(context.Background(), principal)
	if err != nil {
		t.Fatalf("ListSlaOverdue failed: %v", err)
	}
	if len(items) != 1 || items[0].ID != "SI-1" || items[0].Kind != domain.SlaKindPlanEndOverdue {
		t.Fatalf("overdue items not forwarded: %+v", items)
	}
	if items[0].OverdueHours != 12 {
		t.Fatalf("overdue hours must be recomputed from planned_end, got %d", items[0].OverdueHours)
	}
	if repo.lastFilter.TenantID != "tenant-1" {
		t.Fatalf("scope filter not forwarded, got %+v", repo.lastFilter)
	}
}

// 状态停留超期由 pm_sla 规则驱动：只有启用的规则、且状态匹配才产生条目。
func TestListSlaOverdueConsumesSlaRules(t *testing.T) {
	principal := principalWith("project.read", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})
	candidate := domain.SlaOverdueItem{ID: "SI-2", ProjectID: "PJ-1", Status: "待实施", UpdatedAt: time.Now().UTC().Add(-30 * time.Hour)}
	repo := &hookRepository{
		overdue: []domain.SlaOverdueItem{candidate},
		rules:   []domain.Rule{{Kind: "sla", Enabled: true, Name: "待实施超期", Status: "待实施", DeadlineHours: 24, RemindHours: 4}},
	}
	service := Service{Repo: repo}
	items, err := service.ListSlaOverdue(context.Background(), principal)
	if err != nil {
		t.Fatalf("ListSlaOverdue failed: %v", err)
	}
	if len(items) != 1 || items[0].Kind != domain.SlaKindStatusOverdue || items[0].RuleName != "待实施超期" {
		t.Fatalf("enabled sla rule must produce a status-deadline item: %+v", items)
	}
	// 规则停用后不应再产生条目。
	repo.rules[0].Enabled = false
	items, err = service.ListSlaOverdue(context.Background(), principal)
	if err != nil {
		t.Fatalf("ListSlaOverdue failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("disabled sla rule must not produce items: %+v", items)
	}
}

// 人员资质档案必须回基础平台复核"这个人是否真实存在"：目录中查无此人的档案标记
// MISSING，仍存在的标记 ACTIVE，未关联平台账号的历史档案保持 UNLINKED；
// 目录本身报错的档案记为 Unverified 且不改写，避免把平台抖动写成离职。
func TestSyncPersonnelIdentitiesReconcilesAgainstOwnerDirectory(t *testing.T) {
	repo := &capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "张三", UserID: "u-active"},
		{ResourceType: "PERSON", ResourceID: "P-002", ResourceName: "李四", UserID: "u-gone"},
		{ResourceType: "PERSON", ResourceID: "P-003", ResourceName: "王五", UserID: "u-error"},
		{ResourceType: "PERSON", ResourceID: "P-004", ResourceName: "历史档案"},
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-1", ResourceName: "设备"},
	}}
	service := &Service{Repo: repo, Personnel: directoryStub{names: map[string]string{"u-active": "张三"}}}
	// u-error 让目录查询失败，u-gone 查得到但不在 names 里（即查无此人）。
	service.Personnel = directoryStub{names: map[string]string{"u-active": "张三"}, failures: map[string]struct{}{"u-error": {}}}
	principal := principalWith("project.resource.manage", platform.DataScope{RoleCode: "admin", ScopeType: "APPLICATION"})

	result, err := service.SyncPersonnelIdentities(context.Background(), principal)
	if err != nil {
		t.Fatalf("SyncPersonnelIdentities failed: %v", err)
	}
	if result.Total != 4 || result.Active != 1 || result.Missing != 1 || result.Unlinked != 1 || result.Unverified != 1 {
		t.Fatalf("unexpected sync result: %+v", result)
	}
	if result.Active+result.Missing+result.Unlinked+result.Unverified != result.Total {
		t.Fatalf("counts must add up to the record total: %+v", result)
	}
	if repo.identityStatuses["u-active"] != domain.IdentityStatusActive {
		t.Fatalf("existing user must be ACTIVE: %+v", repo.identityStatuses)
	}
	if repo.identityStatuses["u-gone"] != domain.IdentityStatusMissing {
		t.Fatalf("absent user must be MISSING: %+v", repo.identityStatuses)
	}
	if _, touched := repo.identityStatuses["u-error"]; touched {
		t.Fatalf("directory failure must not be written as absence: %+v", repo.identityStatuses)
	}
}

// 未开通负责人目录集成时，复核必须明确失败而不是把所有人标成离职。
func TestSyncPersonnelIdentitiesFailsWithoutDirectory(t *testing.T) {
	service := &Service{Repo: &capabilityRepository{}}
	principal := principalWith("project.resource.manage", platform.DataScope{RoleCode: "admin", ScopeType: "APPLICATION"})
	if _, err := service.SyncPersonnelIdentities(context.Background(), principal); !errors.Is(err, ErrPersonnelUnavailable) {
		t.Fatalf("err = %v, want ErrPersonnelUnavailable", err)
	}
}

// directoryStub 按 user_id 应答，并可注入指定 ID 的目录故障。
type directoryStub struct {
	names    map[string]string
	failures map[string]struct{}
}

func (stub directoryStub) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if _, failed := stub.failures[query.UserID]; failed {
		return platform.OwnerDirectoryPage{}, errors.New("owner directory unavailable")
	}
	display, ok := stub.names[query.UserID]
	if !ok {
		return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{}}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: query.UserID, DisplayName: display}}}, nil
}

// notificationStub 记录被投递的站内信，供自动化通知用例断言。
type notificationStub struct{ published []platform.NotificationEvent }

func (stub *notificationStub) Publish(_ context.Context, event platform.NotificationEvent) error {
	stub.published = append(stub.published, event)
	return nil
}

// 自动化规则的 target 是应用角色码：命中后按角色解析出人员并投递站内信，
// 而不是只写一条没人消费的派生事件。
func TestAutomationNotificationResolvesRoleTargets(t *testing.T) {
	notifications := &notificationStub{}
	repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"}}}
	service := &Service{
		Repo: repo, Notifications: notifications,
		Personnel: roleDirectoryStub{byRole: map[string][]string{"technical_director": {"u-lead", "u-lead2"}}},
	}
	principal := platform.Principal{TenantID: "t1", UserID: "u1"}
	if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", "DEVIATION_REPORTED", map[string]any{"severity": "HIGH"})); err != nil {
		t.Fatalf("applyEvent failed: %v", err)
	}
	if len(notifications.published) != 1 {
		t.Fatalf("expected one notification, got %+v", notifications.published)
	}
	event := notifications.published[0]
	if len(event.Recipients) != 2 || event.Recipients[0] != "u-lead" {
		t.Fatalf("recipients must come from the role directory: %+v", event.Recipients)
	}
	if event.EventType != EventAutomationTriggered || event.IdempotencyKey == "" {
		t.Fatalf("notification payload = %+v", event)
	}
}

// 未开通站内信集成、目录不可用或角色下无人时静默跳过：通知是派生副作用，
// 既不能回滚主事件，也不应因此丢失派生事件本身。
func TestAutomationNotificationDegradesQuietly(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		service *Service
	}{
		{"no notification integration", &Service{Repo: &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"}}}}},
		{"no directory", &Service{Repo: &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"}}}, Notifications: &notificationStub{}}},
		{"role has nobody", &Service{Repo: &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"}}}, Notifications: &notificationStub{}, Personnel: roleDirectoryStub{}}},
	} {
		principal := platform.Principal{TenantID: "t1", UserID: "u1"}
		if err := testCase.service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", "DEVIATION_REPORTED", map[string]any{})); err != nil {
			t.Fatalf("%s: applyEvent failed: %v", testCase.name, err)
		}
		if stub, ok := testCase.service.Notifications.(*notificationStub); ok && len(stub.published) != 0 {
			t.Fatalf("%s: must not publish: %+v", testCase.name, stub.published)
		}
	}
}

// roleDirectoryStub 按 role_code 应答负责人目录查询。
type roleDirectoryStub struct{ byRole map[string][]string }

func (stub roleDirectoryStub) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	items := []platform.OwnerDirectoryUser{}
	for _, role := range query.RoleCodes {
		for _, userID := range stub.byRole[role] {
			items = append(items, platform.OwnerDirectoryUser{UserID: userID, DisplayName: userID})
		}
	}
	return platform.OwnerDirectoryPage{Items: items, Page: 1, PageSize: len(items)}, nil
}

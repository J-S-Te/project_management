package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	// 待制定计划同样是合法入口。
	alsoReady := ready
	alsoReady.Status = "待制定计划"
	if err := CheckImplementationPlanPrecondition(planPreconditionOf(alsoReady)); err != nil {
		t.Fatalf("待制定计划 rejected: %v", err)
	}

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

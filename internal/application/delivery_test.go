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

type supplementContractVerifier struct {
	contract platform.ApprovedContract
	err      error
}

func (v supplementContractVerifier) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return nil, v.err
}
func (v supplementContractVerifier) ListReferences(context.Context, string, int) ([]platform.ApprovedContract, string, error) {
	return nil, "", v.err
}
func (v supplementContractVerifier) Get(context.Context, string) (platform.ApprovedContract, error) {
	return v.contract, v.err
}
func (v supplementContractVerifier) GetServiceItems(context.Context, string) (platform.ApprovedContractServiceCatalog, error) {
	return platform.ApprovedContractServiceCatalog{}, v.err
}

func TestSupplementAgreementMustBeApprovedDistinctAndSameCustomer(t *testing.T) {
	project := domain.Project{ContractID: "CONTRACT-1", CustomerID: "CUSTOMER-1", Customer: "示例客户"}
	cases := []struct {
		name        string
		id          string
		contract    platform.ApprovedContract
		want        error
		messagePart string
	}{
		{name: "same as original", id: "CONTRACT-1", want: ErrValidation, messagePart: "不能与原合同相同"},
		{name: "not approved", id: "SUP-1", contract: platform.ApprovedContract{ID: "SUP-1", CustomerID: "CUSTOMER-1", Status: "draft"}, want: ErrPrecondition, messagePart: "尚未审批通过"},
		{name: "missing customer identity", id: "SUP-1", contract: platform.ApprovedContract{ID: "SUP-1", CustomerName: "示例客户", Status: "approved", ApprovalPassed: true}, want: ErrPrecondition, messagePart: "同一客户"},
		{name: "other customer", id: "SUP-1", contract: platform.ApprovedContract{ID: "SUP-1", CustomerID: "CUSTOMER-2", Status: "approved", ApprovalPassed: true}, want: ErrPrecondition, messagePart: "同一客户"},
		{name: "approved supplement", id: "SUP-1", contract: platform.ApprovedContract{ID: "SUP-1", CustomerID: "CUSTOMER-1", Status: "approved", ApprovalPassed: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &Service{Contracts: supplementContractVerifier{contract: testCase.contract}}
			err := service.verifyApprovedSupplementContract(context.Background(), project, testCase.id)
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("approved supplement rejected: %v", err)
				}
				return
			}
			if !errors.Is(err, testCase.want) || !strings.Contains(UserMessage(err), testCase.messagePart) {
				t.Fatalf("error = %v, want %v containing %q", err, testCase.want, testCase.messagePart)
			}
		})
	}
	t.Run("legacy project matches authoritative customer name", func(t *testing.T) {
		service := &Service{Contracts: supplementContractVerifier{contract: platform.ApprovedContract{ID: "SUP-1", CustomerName: "示例客户", Status: "approved", ApprovalPassed: true}}}
		if err := service.verifyApprovedSupplementContract(context.Background(), domain.Project{Customer: "示例客户"}, "SUP-1"); err != nil {
			t.Fatalf("legacy project supplement rejected: %v", err)
		}
	})
}

func TestNormalizeTravelArrangementUsesBusinessModesInsteadOfFreeTextIDs(t *testing.T) {
	item := domain.ServiceItem{
		ID: "SI-1", ProjectID: "PJ-1", TeamLeadID: "lead-1", ProjectManagerID: "pm-1", EngineerIDs: []string{"eng-1"},
	}

	noTravel, err := normalizeTravelArrangement(domain.PreparationInput{Travel: domain.TravelArrangementInput{
		Mode: domain.TravelModeNoTravel, NoTravelReason: "客户现场位于本市，当日往返",
	}}, item, nil)
	if err != nil || noTravel.Mode != domain.TravelModeNoTravel || noTravel.NoTravelReason == "" || noTravel.RequestID != "" {
		t.Fatalf("no-travel normalization = %+v, err = %v", noTravel, err)
	}
	if _, err := normalizeTravelArrangement(domain.PreparationInput{Travel: domain.TravelArrangementInput{Mode: domain.TravelModeNoTravel}}, item, nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing no-travel reason error = %v, want validation", err)
	}

	events := []domain.DeliveryEvent{{
		ID: "EV-TRIP", ProjectID: "PJ-1", Type: EventPreparationStarted,
		Payload: map[string]any{"travel_request_id": "TRIP-20260920-ABC123"},
	}}
	existing, err := normalizeTravelArrangement(domain.PreparationInput{Travel: domain.TravelArrangementInput{
		Mode: domain.TravelModeExisting, ReferenceEventID: "EV-TRIP", RequestID: "browser-must-not-win",
	}}, item, events)
	if err != nil || existing.RequestID != "TRIP-20260920-ABC123" || existing.ReferenceEventID != "EV-TRIP" {
		t.Fatalf("existing normalization = %+v, err = %v", existing, err)
	}
	if _, err := normalizeTravelArrangement(domain.PreparationInput{Travel: domain.TravelArrangementInput{
		Mode: domain.TravelModeExisting, ReferenceEventID: "EV-OTHER",
	}}, item, events); !errors.Is(err, ErrValidation) {
		t.Fatalf("foreign existing travel error = %v, want validation", err)
	}

	created, err := normalizeTravelArrangement(domain.PreparationInput{Travel: domain.TravelArrangementInput{
		Mode: domain.TravelModeNew, Origin: "杭州", Destination: "上海",
		DepartureDate: "2026-09-25", ReturnDate: "2026-09-27", TravelerIDs: []string{"pm-1", "eng-1", "pm-1"},
	}}, item, nil)
	if err != nil || !strings.HasPrefix(created.RequestID, "TRIP-") || len(created.TravelerIDs) != 2 {
		t.Fatalf("new travel normalization = %+v, err = %v", created, err)
	}
	if _, err := normalizeTravelArrangement(domain.PreparationInput{Travel: domain.TravelArrangementInput{
		Mode: domain.TravelModeNew, Origin: "杭州", Destination: "上海",
		DepartureDate: "2026-09-25", ReturnDate: "2026-09-24", TravelerIDs: []string{"outsider"},
	}}, item, nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid new travel error = %v, want validation", err)
	}
}

func TestNormalizeTravelArrangementKeepsLegacyClientCompatible(t *testing.T) {
	travel, err := normalizeTravelArrangement(domain.PreparationInput{TravelRequestID: " TRIP-LEGACY-1 "}, domain.ServiceItem{}, nil)
	if err != nil || travel.Mode != domain.TravelModeExisting || travel.RequestID != "TRIP-LEGACY-1" {
		t.Fatalf("legacy normalization = %+v, err = %v", travel, err)
	}
}

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

// 实施计划的人员清单：至少一名人员、资源必须命中启用的能力档案，
// 使用时段落在计划内；人员资质日期不限制派工。
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
	// 人员行留空使用时段表示全程；人员快照不再携带已废弃的有效期限制。
	if resources[0].ResourceName != "王明" || resources[0].ValidUntil != "" || resources[0].WindowStart != "" {
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.resolvePlanPersonnel(context.Background(), repo, "tenant-1", tc.rows, start, end)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("error=%v, want contains %q", err, tc.expected)
			}
		})
	}
	resources, err := service.resolvePlanPersonnel(context.Background(), repo, "tenant-1", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-003", WindowStart: "2026-08-10", WindowEnd: "2026-08-20"}}, start, end)
	if err != nil || len(resources) != 1 {
		t.Fatalf("expired personnel qualification must remain assignable: resources=%+v err=%v", resources, err)
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

	// 首尾相接算冲突：使用时段两端都含当日，他人占用到 8-20，则 8-20 当天已被占用。
	// （此前按半开区间判定，把"结束日当天"让给了下一段，与录入人的理解不一致。）
	if _, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF",
		[]domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", WindowStart: "2026-08-20", WindowEnd: "2026-08-22"}}, start, end); !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("touching windows must conflict because both include the end day, got %v", err)
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

// 实施准备允许无需设备；一旦选择设备，仍要求命中有效档案且检定有效期覆盖使用时段。
func TestResolvePreparationEquipmentValidatesCatalogAndCalibration(t *testing.T) {
	repo := &capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "超期设备", Status: "ACTIVE", ValidUntil: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-002", ResourceName: "停用设备", Status: "DISABLED"},
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-003", ResourceName: "检定尚未生效设备", Status: "ACTIVE", ValidFrom: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)},
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
		{"人员不属于准备阶段", []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: "P-001"}}, "资源类型与当前阶段不匹配"},
		{"停用设备", []domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-002"}}, "不在有效的能力档案中"},
		{"检定有效期不覆盖", []domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001"}}, "不覆盖使用时段截止日"},
		{"检定尚未生效", []domain.PlanResourceInput{{ResourceType: "EQUIPMENT", ResourceID: "EQ-003"}}, "不覆盖使用时段开始日"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF", tc.rows, start, end)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("error=%v, want contains %q", err, tc.expected)
			}
		})
	}

	rows, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF", nil, start, end)
	if err != nil {
		t.Fatalf("empty equipment list must be accepted: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("empty equipment list = %#v, want non-nil empty snapshot", rows)
	}
}

func TestDeriveCapabilityEffectiveStatusMarksExpiredEquipmentInvalid(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		item   domain.Capability
		status string
	}{
		{name: "expired", item: domain.Capability{ResourceType: "EQUIPMENT", Status: "ACTIVE", ValidUntil: time.Date(2026, time.September, 10, 23, 59, 0, 0, time.UTC)}, status: capabilityEffectiveExpired},
		{name: "last valid day is inclusive", item: domain.Capability{ResourceType: "EQUIPMENT", Status: "ACTIVE", ValidUntil: time.Date(2026, time.September, 17, 0, 0, 0, 0, time.UTC)}, status: capabilityEffectiveActive},
		{name: "future", item: domain.Capability{ResourceType: "EQUIPMENT", Status: "ACTIVE", ValidFrom: time.Date(2026, time.September, 18, 0, 0, 0, 0, time.UTC)}, status: capabilityEffectiveNotYetEffective},
		{name: "manual disable wins", item: domain.Capability{ResourceType: "EQUIPMENT", Status: "DISABLED", ValidUntil: time.Date(2027, time.September, 17, 0, 0, 0, 0, time.UTC)}, status: capabilityEffectiveDisabled},
		{name: "person ignores dates", item: domain.Capability{ResourceType: "PERSON", Status: "ACTIVE", ValidUntil: time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)}, status: capabilityEffectiveActive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := deriveCapabilityEffectiveStatus(test.item, now)
			if got.EffectiveStatus != test.status {
				t.Fatalf("effective status = %q, want %q", got.EffectiveStatus, test.status)
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

func TestDeleteEquipmentProtectsActiveReservations(t *testing.T) {
	manager := principalWith("project.device.manage", platform.DataScope{RoleCode: "device_admin", ScopeType: "APPLICATION"})

	t.Run("deletes an unreserved equipment record", func(t *testing.T) {
		repo := &capabilityRepository{capabilities: []domain.Capability{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备"}}}
		service := &Service{Repo: repo}
		if err := service.DeleteEquipment(context.Background(), manager, "EQ-001"); err != nil {
			t.Fatal(err)
		}
		if len(repo.capabilities) != 0 {
			t.Fatalf("equipment was not removed: %+v", repo.capabilities)
		}
	})

	t.Run("rejects deletion while an implementation plan still holds the equipment", func(t *testing.T) {
		repo := &capabilityRepository{
			capabilities: []domain.Capability{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备"}},
			reservations: []domain.EquipmentReservation{{ServiceItemID: "SI-001", ResourceID: "EQ-001", WindowStart: "2026-09-01", WindowEnd: "2026-09-30"}},
		}
		service := &Service{Repo: repo}
		err := service.DeleteEquipment(context.Background(), manager, "EQ-001")
		if !errors.Is(err, ErrResourceConflict) || !strings.Contains(err.Error(), "SI-001") {
			t.Fatalf("error=%v, want an actionable equipment reservation conflict", err)
		}
		if len(repo.capabilities) != 1 {
			t.Fatalf("reserved equipment must remain: %+v", repo.capabilities)
		}
	})
}

// 通用资质入口只维护人员；设备新建、修改和删除必须统一走设备能力入口。
func TestQualificationUpsertAndImportRejectEquipment(t *testing.T) {
	repo := &capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备", Codes: []string{"c1"}, Status: "ACTIVE", UsageScope: domain.EquipmentUsageCompanyOnly},
	}}
	service := &Service{Repo: repo}
	manager := principalWith("project.resource.manage", platform.DataScope{RoleCode: "project_manager", ScopeType: "APPLICATION"})

	if _, err := service.UpsertCapability(context.Background(), manager, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备改名", Codes: []string{"c1"}}); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "设备能力") {
		t.Fatalf("通用资质入口应拒绝设备，error=%v", err)
	}
	result, err := service.ImportCapabilities(context.Background(), manager, []domain.Capability{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "机房设备", Codes: []string{"c1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 0 || result.Skipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "设备能力") {
		t.Fatalf("设备 CSV 应被逐行拒绝，result=%+v", result)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("通用资质入口不得写入设备：%+v", repo.saved)
	}
}

// 新增人员资质不能把浏览器填写的名字当作身份数据：必须由基础平台目录按 user_id
// 精确确认，并以目录的显示名入库。这样人员改名、前端构造姓名或已失效账号都不会污染台账。
func TestUpsertPersonCapabilityUsesPlatformDirectoryIdentity(t *testing.T) {
	repository := &capabilityRepository{}
	directory := &personnelStub{page: platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: "platform-user-1", DisplayName: "平台张三"}}}}
	service := &Service{Repo: repository, Personnel: directory}
	manager := principalWith("project.resource.manage", platform.DataScope{RoleCode: "quality_manager", ScopeType: "APPLICATION"})

	saved, err := service.UpsertCapability(context.Background(), manager, domain.Capability{
		ResourceType: "PERSON", ResourceID: "P-0001", ResourceName: "客户端伪造姓名", UserID: "platform-user-1", Codes: []string{"QUAL-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.UserID != "platform-user-1" || saved.ResourceName != "平台张三" || saved.IdentityStatus != domain.IdentityStatusActive {
		t.Fatalf("saved=%+v", saved)
	}
	if len(repository.saved) != 1 || repository.saved[0].ResourceName != "平台张三" {
		t.Fatalf("repository saved=%+v", repository.saved)
	}
	if directory.lastQuery.UserID != "platform-user-1" {
		t.Fatalf("directory query=%+v", directory.lastQuery)
	}

	directory.page = platform.OwnerDirectoryPage{}
	if _, err := service.UpsertCapability(context.Background(), manager, domain.Capability{ResourceType: "PERSON", ResourceID: "P-0002", ResourceName: "任意姓名", UserID: "missing-user", Codes: []string{"QUAL-1"}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing platform person error=%v, want validation", err)
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
	duplicate       bool
	findContractIDs []string
}

func (r *duplicateContractRepository) FindProjectByContractVersion(_ context.Context, _ platform.ScopeFilter, contractID, _ string) (domain.Project, error) {
	r.findContractIDs = append(r.findContractIDs, contractID)
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
func (r *duplicateContractRepository) DeleteEquipment(context.Context, string, string) error {
	return ErrNotFound
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
			ContractID: "CT-001", ContractNumber: "HT-2026-001", ContractVersion: "v1.0", Customer: "客户A", EffectiveAt: time.Now().UTC(),
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
	if first.ContractID != "CT-001" || first.Contract != "HT-2026-001" {
		t.Fatalf("contract identity/display mapping = %+v", first)
	}
	for _, contractID := range activeRepo.findContractIDs {
		if contractID != "CT-001" {
			t.Fatalf("idempotency lookup used %q, want stable contract id", contractID)
		}
	}
}

// hookRepository 同时充当 Repository 与 DeliveryRepository，记录派生事件并保留规则/超期数据，
// 供 automations / warning-rules / sla 钩子测试使用。
type hookRepository struct {
	capabilityRepository
	rules    []domain.Rule
	events   []domain.DeliveryEvent
	overdue  []domain.SlaOverdueItem
	enqueued []domain.NotificationMessage
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
func (r *hookRepository) EnqueueNotification(_ context.Context, _ string, message domain.NotificationMessage) (bool, error) {
	r.enqueued = append(r.enqueued, message)
	return true, nil
}

type assignmentRevokeRepository struct {
	capabilityRepository
	item   domain.ServiceItem
	events []domain.DeliveryEvent
}

func (r *assignmentRevokeRepository) GetServiceItem(_ context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	r.lastFilter = filter
	if r.item.ID != id {
		return domain.ServiceItem{}, ErrNotFound
	}
	return r.item, nil
}
func (r *assignmentRevokeRepository) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.events = append(r.events, event)
	return nil
}

type reportCorrectionRepository struct {
	capabilityRepository
	item   domain.ServiceItem
	events []domain.DeliveryEvent
}

func (r *reportCorrectionRepository) GetServiceItem(_ context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	r.lastFilter = filter
	if r.item.ID != id {
		return domain.ServiceItem{}, ErrNotFound
	}
	return r.item, nil
}

func (r *reportCorrectionRepository) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.events = append(r.events, event)
	return nil
}

func (r *reportCorrectionRepository) ListDeliveryEvents(_ context.Context, filter platform.ScopeFilter, projectID string) ([]domain.DeliveryEvent, error) {
	r.lastFilter = filter
	if projectID != r.item.ProjectID {
		return nil, ErrNotFound
	}
	return append([]domain.DeliveryEvent(nil), r.events...), nil
}

func reportCorrectionPrincipal(userID, permission string) platform.Principal {
	return platform.Principal{
		TenantID:    "tenant-1",
		IdentityID:  userID,
		UserID:      userID,
		Permissions: map[string]bool{permission: true},
		DataScopes:  []platform.DataScope{{RoleCode: "project-regression", ScopeType: "APPLICATION"}},
	}
}

func TestFieldExecutionRequiresExplicitStartBeforeRecords(t *testing.T) {
	repository := &assignmentRevokeRepository{item: domain.ServiceItem{
		ID: "SI-FIELD-1", ProjectID: "PJ-FIELD-1", Status: "实施准备中", Version: 7,
	}}
	service := Service{Repo: repository}
	manager := reportCorrectionPrincipal("project-manager-1", "project.field.execute")

	err := service.SubmitFieldRecord(context.Background(), manager, repository.item.ID, domain.FieldRecordInput{
		RawData: "现场测评记录", Environment: "客户现场", ExpectedVersion: 7,
	})
	if !errors.Is(err, ErrPrecondition) {
		t.Fatalf("record before explicit start error=%v, want ErrPrecondition", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("record before start persisted %d events", len(repository.events))
	}

	if err := service.StartFieldExecution(context.Background(), manager, repository.item.ID, domain.FieldStartInput{ExpectedVersion: 7}); err != nil {
		t.Fatalf("start field execution: %v", err)
	}
	if len(repository.events) != 1 || repository.events[0].Type != EventFieldStarted {
		t.Fatalf("start events=%+v, want one %s", repository.events, EventFieldStarted)
	}

	// 仓储在真实事务中会推进状态和版本；单元桩同步该结果后验证记录仅落事件，
	// 不再承担“实施准备中 -> 实施中”的隐式状态迁移。
	repository.item.Status = "实施中"
	repository.item.Version = 8
	if err := service.SubmitFieldRecord(context.Background(), manager, repository.item.ID, domain.FieldRecordInput{
		RawData: "现场测评记录", Environment: "客户现场", ExpectedVersion: 8,
	}); err != nil {
		t.Fatalf("submit field record after start: %v", err)
	}
	if len(repository.events) != 2 || repository.events[1].Type != EventFieldRecordSubmitted {
		t.Fatalf("record events=%+v, want %s after %s", repository.events, EventFieldRecordSubmitted, EventFieldStarted)
	}
}

func TestFieldExecutionStartRejectsWrongStateAndStaleVersion(t *testing.T) {
	repository := &assignmentRevokeRepository{item: domain.ServiceItem{
		ID: "SI-FIELD-2", ProjectID: "PJ-FIELD-2", Status: "实施准备中", Version: 4,
	}}
	service := Service{Repo: repository}
	manager := reportCorrectionPrincipal("project-manager-1", "project.field.execute")

	if err := service.StartFieldExecution(context.Background(), manager, repository.item.ID, domain.FieldStartInput{ExpectedVersion: 3}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale start error=%v, want ErrConflict", err)
	}
	repository.item.Status = "实施中"
	if err := service.StartFieldExecution(context.Background(), manager, repository.item.ID, domain.FieldStartInput{ExpectedVersion: 4}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("duplicate start error=%v, want ErrPrecondition", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("rejected starts persisted %d events", len(repository.events))
	}
}

func TestReportCorrectionEnforcesStateVersionAndTwoPersonApproval(t *testing.T) {
	repository := &reportCorrectionRepository{item: domain.ServiceItem{
		ID: "SI-REPORT-1", ProjectID: "PJ-REPORT-1", Status: "现场实施完成",
		ReportStatus: "ARCHIVED", ReportRevision: 3, Version: 12,
	}}
	service := Service{Repo: repository}
	requester := reportCorrectionPrincipal("project-manager-1", "project.report.correction.request")
	approver := reportCorrectionPrincipal("technical-director-1", "project.report.correction.approve")

	if _, err := service.RequestReportCorrection(context.Background(), requester, repository.item.ID, domain.ReportCorrectionRequestInput{
		Reason: "客户名称需要修正", ExpectedVersion: 11,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale correction request error=%v, want ErrConflict", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("stale request persisted %d events", len(repository.events))
	}

	requestID, err := service.RequestReportCorrection(context.Background(), requester, repository.item.ID, domain.ReportCorrectionRequestInput{
		Reason: "  客户名称需要修正  ", ExpectedVersion: 12,
	})
	if err != nil {
		t.Fatalf("request report correction: %v", err)
	}
	if requestID == "" || len(repository.events) != 1 {
		t.Fatalf("request id=%q events=%d, want one immutable request event", requestID, len(repository.events))
	}
	requestEvent := repository.events[0]
	if requestEvent.Type != EventReportCorrectionRequested || requestEvent.ActorUserID != requester.UserID {
		t.Fatalf("request event=%+v", requestEvent)
	}
	if got := payloadText(requestEvent.Payload, "reason"); got != "客户名称需要修正" {
		t.Fatalf("trimmed reason=%q", got)
	}
	if got, ok := requestEvent.Payload["old_revision"].(uint64); !ok || got != 3 {
		t.Fatalf("old revision=%v, want uint64(3)", requestEvent.Payload["old_revision"])
	}
	if repository.item.ReportStatus != "ARCHIVED" || repository.item.ReportRevision != 3 {
		t.Fatalf("request must not invalidate published report: %+v", repository.item)
	}

	sameActor := reportCorrectionPrincipal(requester.UserID, "project.report.correction.approve")
	if err := service.DecideReportCorrection(context.Background(), sameActor, repository.item.ID, requestID, domain.ReportCorrectionDecisionInput{
		Decision: "APPROVED", Comment: "同意", ExpectedVersion: 12,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("same actor approval error=%v, want ErrForbidden", err)
	}
	if len(repository.events) != 1 {
		t.Fatalf("same actor approval persisted %d events", len(repository.events))
	}

	if err := service.DecideReportCorrection(context.Background(), approver, repository.item.ID, requestID, domain.ReportCorrectionDecisionInput{
		Decision: "approved", Comment: "  同意更正  ", ExpectedVersion: 12,
	}); err != nil {
		t.Fatalf("approve report correction: %v", err)
	}
	if len(repository.events) != 2 {
		t.Fatalf("events=%d, want request and approval", len(repository.events))
	}
	decisionEvent := repository.events[1]
	if decisionEvent.Type != EventReportCorrectionApproved || decisionEvent.ActorUserID != approver.UserID {
		t.Fatalf("decision event=%+v", decisionEvent)
	}
	if payloadText(decisionEvent.Payload, "request_id") != requestID ||
		payloadText(decisionEvent.Payload, "report_correction_requester_id") != requester.UserID ||
		payloadText(decisionEvent.Payload, "comment") != "同意更正" {
		t.Fatalf("decision payload=%+v", decisionEvent.Payload)
	}
	if oldRevision, ok := decisionEvent.Payload["old_revision"].(uint64); !ok || oldRevision != 3 {
		t.Fatalf("decision old_revision=%v, want uint64(3)", decisionEvent.Payload["old_revision"])
	}
	if expectedVersion, ok := decisionEvent.Payload["expected_version"].(uint64); !ok || expectedVersion != 12 {
		t.Fatalf("decision expected_version=%v, want uint64(12)", decisionEvent.Payload["expected_version"])
	}
}

func TestReportCorrectionRejectsUnauthorizedInvalidAndUnpublishedRequests(t *testing.T) {
	base := domain.ServiceItem{ID: "SI-REPORT-2", ProjectID: "PJ-REPORT-2", Status: "现场实施完成", ReportStatus: "ISSUED", ReportRevision: 1, Version: 4}
	requester := reportCorrectionPrincipal("project-manager-1", "project.report.correction.request")

	tests := []struct {
		name      string
		item      domain.ServiceItem
		principal platform.Principal
		input     domain.ReportCorrectionRequestInput
		want      error
	}{
		{name: "missing permission", item: base, principal: reportCorrectionPrincipal("viewer-1", "project.read"), input: domain.ReportCorrectionRequestInput{Reason: "修正内容"}, want: ErrForbidden},
		{name: "missing reason", item: base, principal: requester, input: domain.ReportCorrectionRequestInput{}, want: ErrValidation},
		{name: "field not completed", item: func() domain.ServiceItem { item := base; item.Status = "实施中"; return item }(), principal: requester, input: domain.ReportCorrectionRequestInput{Reason: "修正内容"}, want: ErrPrecondition},
		{name: "report not published", item: func() domain.ServiceItem { item := base; item.ReportStatus = "REVIEWED"; return item }(), principal: requester, input: domain.ReportCorrectionRequestInput{Reason: "修正内容"}, want: ErrPrecondition},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &reportCorrectionRepository{item: testCase.item}
			service := Service{Repo: repository}
			_, err := service.RequestReportCorrection(context.Background(), testCase.principal, testCase.item.ID, testCase.input)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error=%v, want %v", err, testCase.want)
			}
			if len(repository.events) != 0 {
				t.Fatalf("invalid request persisted %d events", len(repository.events))
			}
		})
	}
}

type assignmentValidationRepository struct {
	assignmentRevokeRepository
	foundCapabilities []domain.Capability
}

func (r *assignmentValidationRepository) ListServiceItems(_ context.Context, _ platform.ScopeFilter, _ string) ([]domain.ServiceItem, error) {
	return []domain.ServiceItem{r.item}, nil
}

func (r *assignmentValidationRepository) FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error) {
	return r.foundCapabilities, nil
}

// 客户端 required_codes 只是附加要求，不能替换检测类别已经落到服务项的必检码。
// 否则调用者可少传 CORE 后只提交 EXTRA，绕过服务端定义的资质边界。
func TestAssignExecutionTeamCannotShrinkPersistedRequiredCodes(t *testing.T) {
	repository := &assignmentValidationRepository{assignmentRevokeRepository: assignmentRevokeRepository{
		item: domain.ServiceItem{ID: "SI-1", ProjectID: "PJ-1", RequiredCodes: []string{"core"}},
	}}
	repository.foundCapabilities = []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-MANAGER", UserID: "PM-1", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE"},
		{ResourceType: "PERSON", ResourceID: "P-ENGINEER", UserID: "ENG-1", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE", Codes: []string{"EXTRA"}},
	}
	principal := platform.Principal{
		TenantID: "t1", UserID: "lead-1",
		Permissions: map[string]bool{"project.execution.assign": true},
		DataScopes:  []platform.DataScope{{RoleCode: "team_lead", ScopeType: "APPLICATION"}},
	}
	service := Service{Repo: repository, Personnel: roleDirectoryStub{byRole: map[string][]string{
		assignmentRoleProjectManager: {"PM-1"},
		assignmentRoleEngineer:       {"ENG-1"},
	}}}

	result, err := service.AssignExecutionTeam(context.Background(), principal, "SI-1", domain.ExecutionAssignmentInput{
		ProjectManagerID: "PM-1", EngineerIDs: []string{"ENG-1"}, RequiredCodes: []string{"extra"},
	})
	if err != nil {
		t.Fatalf("assign execution team: %v", err)
	}
	if result.Passed || !contains(result.Conflicts, "缺少能力：CORE") {
		t.Fatalf("persisted CORE requirement must not be dropped: %+v", result)
	}
	if len(repository.events) != 1 {
		t.Fatalf("events=%d, want 1", len(repository.events))
	}
	required := payloadTextList(repository.events[0].Payload, "required_codes")
	if len(required) != 2 || required[0] != "CORE" || required[1] != "EXTRA" {
		t.Fatalf("event required_codes=%v, want [CORE EXTRA]", required)
	}
}

func TestAssignmentsRequireQualifiedPeopleAndStorePlatformUserIDs(t *testing.T) {
	repository := &assignmentValidationRepository{assignmentRevokeRepository: assignmentRevokeRepository{
		item: domain.ServiceItem{ID: "SI-1", ProjectID: "PJ-1", Status: "待分配", Version: 1},
	}}
	repository.foundCapabilities = []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-LEAD", UserID: "user-lead", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE", ValidUntil: time.Now().UTC().AddDate(0, 0, -30)},
		{ResourceType: "PERSON", ResourceID: "P-MANAGER", UserID: "user-manager", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE"},
		{ResourceType: "PERSON", ResourceID: "P-ENGINEER", UserID: "user-engineer", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE"},
	}
	principal := platform.Principal{TenantID: "t1", IdentityID: "admin-identity", UserID: "admin-user", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.team.assign": true, "project.execution.assign": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	service := Service{Repo: repository, Personnel: roleDirectoryStub{byRole: map[string][]string{
		assignmentRoleTeamLead:       {"user-lead"},
		assignmentRoleProjectManager: {"user-manager"},
		assignmentRoleEngineer:       {"user-engineer"},
	}}}
	if err := service.AssignTeam(context.Background(), principal, "SI-1", domain.TeamAssignmentInput{TeamLeadID: "P-LEAD", ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if got := payloadText(repository.events[0].Payload, "team_lead_id"); got != "user-lead" {
		t.Fatalf("team lead stored as %q, want platform user id", got)
	}
	repository.events = nil
	result, err := service.AssignExecutionTeam(context.Background(), principal, "SI-1", domain.ExecutionAssignmentInput{ProjectManagerID: "P-MANAGER", EngineerIDs: []string{"P-ENGINEER"}, ExpectedVersion: 1})
	if err != nil || !result.Passed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := payloadText(repository.events[0].Payload, "project_manager_id"); got != "user-manager" {
		t.Fatalf("project manager stored as %q", got)
	}
	engineers := payloadTextList(repository.events[0].Payload, "engineer_ids")
	if len(engineers) != 1 || engineers[0] != "user-engineer" {
		t.Fatalf("engineers=%v", engineers)
	}
	repository.foundCapabilities = nil
	if err := service.AssignTeam(context.Background(), principal, "SI-1", domain.TeamAssignmentInput{TeamLeadID: "unknown"}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("unqualified team lead err=%v", err)
	}
}

func TestAssignmentsRejectQualifiedPeopleWithoutRequiredRole(t *testing.T) {
	repository := &assignmentValidationRepository{assignmentRevokeRepository: assignmentRevokeRepository{
		item: domain.ServiceItem{ID: "SI-1", ProjectID: "PJ-1", Status: "待分配"},
	}, foundCapabilities: []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-LEAD", UserID: "user-qualified", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE"},
	}}
	principal := platform.Principal{TenantID: "t1", UserID: "admin-user", Permissions: map[string]bool{"project.team.assign": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	service := Service{Repo: repository, Personnel: roleDirectoryStub{byRole: map[string][]string{
		assignmentRoleTeamLead: {"another-user"},
	}}}
	err := service.AssignTeam(context.Background(), principal, "SI-1", domain.TeamAssignmentInput{TeamLeadID: "P-LEAD"})
	if !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "团队负责人角色") {
		t.Fatalf("role mismatch error=%v, want team-lead precondition", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("events=%d, role mismatch must not be persisted", len(repository.events))
	}
}

func TestAssignmentRevocationCarriesReasonAndExpectedVersion(t *testing.T) {
	repository := &assignmentRevokeRepository{item: domain.ServiceItem{ID: "SI-1", ProjectID: "PJ-1", Status: "待分配", Version: 7, TeamLeadID: "lead-1", ProjectManagerID: "manager-1", EngineerIDs: []string{"engineer-1"}}}
	principal := platform.Principal{TenantID: "t1", UserID: "operator-1", Permissions: map[string]bool{"project.team.revoke": true, "project.execution.revoke": true}, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}}
	service := Service{Repo: repository}
	if err := service.RevokeTeamAssignment(context.Background(), principal, "SI-1", domain.AssignmentRevokeInput{Reason: "负责人录入错误", ExpectedVersion: 7}); err != nil {
		t.Fatalf("revoke team assignment: %v", err)
	}
	if len(repository.events) != 1 {
		t.Fatalf("events=%d, want 1", len(repository.events))
	}
	event := repository.events[0]
	if event.Type != EventTeamAssignmentRevoked || event.Payload["reason"] != "负责人录入错误" || event.Payload["expected_version"] != uint64(7) {
		t.Fatalf("unexpected revoke event: %#v", event)
	}
	if ids := payloadTextList(event.Payload, "revoked_user_ids"); len(ids) != 3 || ids[0] != "lead-1" || ids[2] != "engineer-1" {
		t.Fatalf("revoke recipients=%#v", ids)
	}
	// 两次命令模拟为用户刷新后在下一业务版本上继续操作。
	repository.item.Version = 8
	if err := service.RevokeExecutionAssignment(context.Background(), principal, "SI-1", domain.AssignmentRevokeInput{Reason: "执行资源需重排", ExpectedVersion: 8}); err != nil {
		t.Fatalf("revoke execution assignment: %v", err)
	}
	if got := repository.events[1].Payload["expected_version"]; got != uint64(8) {
		t.Fatalf("execution expected_version=%#v", got)
	}
	if err := service.RevokeTeamAssignment(context.Background(), principal, "SI-1", domain.AssignmentRevokeInput{ExpectedVersion: 7}); err == nil {
		t.Fatal("missing revoke reason must be rejected")
	}
}

func TestReturnToDecompositionCarriesAuditSnapshotAndRejectsInvalidState(t *testing.T) {
	repository := &assignmentRevokeRepository{item: domain.ServiceItem{ID: "SI-1", ProjectID: "PJ-1", Status: "待分配", Version: 9, TeamLeadID: "lead-1", ProjectManagerID: "manager-1", EngineerIDs: []string{"engineer-1"}}}
	principal := platform.Principal{TenantID: "t1", UserID: "operator-1", Permissions: map[string]bool{"project.decomposition.manage": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	service := Service{Repo: repository}

	if err := service.ReturnToDecomposition(context.Background(), principal, "SI-1", domain.AssignmentRevokeInput{Reason: "拆解范围需要重新确认", ExpectedVersion: 9}); err != nil {
		t.Fatalf("return to decomposition: %v", err)
	}
	if len(repository.events) != 1 {
		t.Fatalf("events=%d, want 1", len(repository.events))
	}
	event := repository.events[0]
	if event.Type != EventDecompositionReturned || event.Payload["reason"] != "拆解范围需要重新确认" || event.Payload["expected_version"] != uint64(9) {
		t.Fatalf("unexpected return event: %#v", event)
	}
	if ids := payloadTextList(event.Payload, "revoked_user_ids"); len(ids) != 3 || ids[0] != "lead-1" || ids[2] != "engineer-1" {
		t.Fatalf("return recipients=%#v", ids)
	}

	repository.item.Status = "待实施"
	if err := service.ReturnToDecomposition(context.Background(), principal, "SI-1", domain.AssignmentRevokeInput{Reason: "错误回退", ExpectedVersion: 9}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("non-pending-allocation return error=%v, want precondition", err)
	}
	repository.item.Status = "待分配"
	if err := service.ReturnToDecomposition(context.Background(), principal, "SI-1", domain.AssignmentRevokeInput{ExpectedVersion: 9}); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing reason error=%v, want validation", err)
	}
}

func TestApplyEventFiresAutomationEventOnlyWhenRuleMatches(t *testing.T) {
	principal := platform.Principal{TenantID: "t1", UserID: "u1"}

	t.Run("enabled matching rule appends AUTOMATION_TRIGGERED", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"}}}
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
		if len(targets) != 1 || targets[0] != "technical_director" {
			t.Fatalf("derived event targets wrong: %+v", triggered.Payload)
		}
	})

	t.Run("legacy free-text action target is ignored", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: EventDeviationReported, Target: "创建整改工单"}}}
		service := Service{Repo: repo}
		if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventDeviationReported, nil)); err != nil {
			t.Fatalf("applyEvent failed: %v", err)
		}
		if len(repo.events) != 1 {
			t.Fatalf("invalid legacy target must not create a derived action: %+v", repo.events)
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
// 计时基准必须是「进入当前状态的时刻」StatusChangedAt，不能用行更新时间 UpdatedAt。
func TestListSlaOverdueConsumesSlaRules(t *testing.T) {
	principal := principalWith("project.read", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})
	candidate := domain.SlaOverdueItem{ID: "SI-2", ProjectID: "PJ-1", Status: "待实施", StatusChangedAt: time.Now().UTC().Add(-30 * time.Hour)}
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
	// 计时基准缺失时不得判超期：零值时间会让 now.Sub 溢出成天文数字，
	// 从而把刚进入状态的项误报为超期。这条断言防止该缺陷回归。
	repo.overdue = []domain.SlaOverdueItem{{ID: "SI-2", ProjectID: "PJ-1", Status: "待实施"}}
	items, err = service.ListSlaOverdue(context.Background(), principal)
	if err != nil {
		t.Fatalf("ListSlaOverdue failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("missing status-change timestamp must not be judged overdue: %+v", items)
	}
	// 行更新时间不参与判定：只有 UpdatedAt 而没有 StatusChangedAt 时同样不应产生条目。
	repo.overdue = []domain.SlaOverdueItem{{ID: "SI-2", ProjectID: "PJ-1", Status: "待实施", UpdatedAt: time.Now().UTC().Add(-30 * time.Hour)}}
	items, err = service.ListSlaOverdue(context.Background(), principal)
	if err != nil {
		t.Fatalf("ListSlaOverdue failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("row update time must not drive sla timing: %+v", items)
	}
	// 规则停用后不应再产生条目。
	repo.overdue = []domain.SlaOverdueItem{candidate}
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
func TestAutomationNotificationResolvesMultipleRoleTargets(t *testing.T) {
	repo := &hookRepository{rules: []domain.Rule{
		{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"},
		{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "quality_manager"},
	}}
	service := &Service{
		Repo: repo,
		Personnel: roleDirectoryStub{byRole: map[string][]string{
			"technical_director": {"u-lead", "u-shared"},
			"quality_manager":    {"u-quality", "u-shared"},
		}},
	}
	principal := platform.Principal{TenantID: "t1", UserID: "u1"}
	if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", "DEVIATION_REPORTED", map[string]any{"severity": "HIGH"})); err != nil {
		t.Fatalf("applyEvent failed: %v", err)
	}
	if len(repo.enqueued) != 1 {
		t.Fatalf("expected one durable notification, got %+v", repo.enqueued)
	}
	event := repo.enqueued[0]
	if len(event.Recipients) != 3 || event.Recipients[0] != "u-lead" || event.Recipients[2] != "u-quality" {
		t.Fatalf("recipients must combine role directories and de-duplicate shared users: %+v", event.Recipients)
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
		{"no directory", &Service{Repo: &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"}}}}},
		{"role has nobody", &Service{Repo: &hookRepository{rules: []domain.Rule{{Enabled: true, Trigger: "DEVIATION_REPORTED", Target: "technical_director"}}}, Personnel: roleDirectoryStub{}}},
	} {
		principal := platform.Principal{TenantID: "t1", UserID: "u1"}
		if err := testCase.service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", "DEVIATION_REPORTED", map[string]any{})); err != nil {
			t.Fatalf("%s: applyEvent failed: %v", testCase.name, err)
		}
		if repository, ok := testCase.service.Repo.(*hookRepository); ok && len(repository.enqueued) != 0 {
			t.Fatalf("%s: must not enqueue: %+v", testCase.name, repository.enqueued)
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

// 指派类事件必须给「被指派人」发站内提醒：提醒要发给需要行动的人，而不是操作者本人。
// 此前项目系统的通知链路 endpoint 与 scope 双错，任何通知都发不出去，本用例锁定该行为。
func TestAssignmentNotificationGoesToAssignees(t *testing.T) {
	repo := &hookRepository{}
	service := &Service{Repo: repo}
	principal := platform.Principal{TenantID: "t1", UserID: "u-actor"}

	if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventTeamAssigned,
		map[string]any{"team_lead_id": "u-lead"})); err != nil {
		t.Fatalf("applyEvent failed: %v", err)
	}
	if len(repo.events) != 1 || repo.events[0].Notification == nil {
		t.Fatalf("team assignment must persist one notification with its event, got %+v", repo.events)
	}
	first := *repo.events[0].Notification
	if first.EventType != EventTeamAssigned || first.Scope != platform.NotificationScopeCrossSystem {
		t.Fatalf("notification must carry event type and platform scope: %+v", first)
	}
	if len(first.Recipients) != 1 || first.Recipients[0] != "u-lead" {
		t.Fatalf("recipient must be the assigned team lead: %+v", first.Recipients)
	}
	if first.IdempotencyKey == "" || first.ReferenceID != "SI-1" {
		t.Fatalf("notification must be idempotent and reference the service item: %+v", first)
	}

	// 执行分配：项目经理与工程师都是被指派人。
	repo.events = nil
	if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventExecutionTeamAssigned,
		map[string]any{"project_manager_id": "u-pm", "engineer_ids": []string{"u-e1", " u-e2 ", ""}})); err != nil {
		t.Fatalf("applyEvent failed: %v", err)
	}
	if len(repo.events) != 1 || repo.events[0].Notification == nil {
		t.Fatalf("execution assignment must persist one notification, got %+v", repo.events)
	}
	recipients := repo.events[0].Notification.Recipients
	if len(recipients) != 3 || recipients[0] != "u-pm" || recipients[1] != "u-e1" || recipients[2] != "u-e2" {
		t.Fatalf("recipients must be project manager plus engineers: %+v", recipients)
	}
}

// 非指派事件不在此路径发提醒（其余业务节点另行按口径补齐），且未开通集成时静默跳过。
func TestAssignmentNotificationStaysQuietOtherwise(t *testing.T) {
	repo := &hookRepository{}
	service := &Service{Repo: repo}
	principal := platform.Principal{TenantID: "t1", UserID: "u-actor"}

	if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventFieldRecordSubmitted,
		map[string]any{"raw_data": "{}"})); err != nil {
		t.Fatalf("applyEvent failed: %v", err)
	}
	if repo.events[0].Notification != nil {
		t.Fatalf("non-assignment event must not attach a notification: %+v", repo.events[0])
	}

	// 指派事件但缺收件人：不发空通知。
	if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventTeamAssigned,
		map[string]any{"team_lead_id": "  "})); err != nil {
		t.Fatalf("applyEvent failed: %v", err)
	}
	if repo.events[len(repo.events)-1].Notification != nil {
		t.Fatalf("assignment without recipients must not attach a notification: %+v", repo.events)
	}

	// 未开通站内信集成：不得 panic，也不影响主事件。
	withoutIntegration := &Service{Repo: &hookRepository{}}
	if err := withoutIntegration.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventTeamAssigned,
		map[string]any{"team_lead_id": "u-lead"})); err != nil {
		t.Fatalf("applyEvent without notification integration failed: %v", err)
	}
}

// 设备占用按「两端含当日」的闭区间判定，与资质有效期同一日期口径：
// [10-01..10-05] 与 [10-05..10-10] 在 10-05 当天重叠，必须算冲突；
// 占用记录时段数据异常时按占用处理，避免整段检查静默失效。
func TestEquipmentUsageConflictsUsesInclusiveDayRange(t *testing.T) {
	day := func(text string) time.Time {
		parsed, err := time.Parse("2006-01-02", text)
		if err != nil {
			t.Fatalf("parse %s: %v", text, err)
		}
		return parsed
	}
	row := domain.PlanResource{ResourceID: "EQ-1", ResourceName: "频谱仪"}
	reservation := func(start, end string) domain.EquipmentReservation {
		return domain.EquipmentReservation{ResourceID: "EQ-1", ServiceItemID: "SI-OTHER", ProjectID: "PJ-OTHER", WindowStart: start, WindowEnd: end}
	}

	// 相邻一天重叠（半开区间会漏判的情况）。
	if conflicts := equipmentUsageConflicts(row, day("2026-10-05"), day("2026-10-10"), []domain.EquipmentReservation{reservation("2026-10-01", "2026-10-05")}); len(conflicts) != 1 {
		t.Fatalf("结束日当天的重叠必须判为冲突，实际 %+v", conflicts)
	}
	// 同一天完全重合。
	if conflicts := equipmentUsageConflicts(row, day("2026-10-05"), day("2026-10-05"), []domain.EquipmentReservation{reservation("2026-10-05", "2026-10-05")}); len(conflicts) != 1 {
		t.Fatalf("同一天必须判为冲突，实际 %+v", conflicts)
	}
	// 完全不重叠：中间隔一天。
	if conflicts := equipmentUsageConflicts(row, day("2026-10-06"), day("2026-10-08"), []domain.EquipmentReservation{reservation("2026-10-01", "2026-10-05")}); len(conflicts) != 0 {
		t.Fatalf("不重叠时段不应判为冲突，实际 %+v", conflicts)
	}
	// 占用记录数据异常：按占用处理并给出可修正的提示。
	conflicts := equipmentUsageConflicts(row, day("2026-10-06"), day("2026-10-08"), []domain.EquipmentReservation{reservation("坏数据", "2026-10-05")})
	if len(conflicts) != 1 || !strings.Contains(conflicts[0], "数据异常") {
		t.Fatalf("异常占用记录必须按占用处理并说明原因，实际 %+v", conflicts)
	}
	// 其它设备不受影响。
	other := domain.EquipmentReservation{ResourceID: "EQ-2", ServiceItemID: "SI-OTHER", WindowStart: "2026-10-01", WindowEnd: "2026-10-05"}
	if conflicts := equipmentUsageConflicts(row, day("2026-10-01"), day("2026-10-05"), []domain.EquipmentReservation{other}); len(conflicts) != 0 {
		t.Fatalf("不同设备不应互判冲突，实际 %+v", conflicts)
	}
}

// 实施计划发布后必须提醒现场实施人员：收件人取自计划的人员清单（只取人员行，设备行不算人）。
func TestImplementationPlanNotificationNotifiesPersonnel(t *testing.T) {
	repo := &hookRepository{}
	service := &Service{Repo: repo}
	principal := platform.Principal{TenantID: "t1", UserID: "u-actor"}
	payload := map[string]any{
		"planned_start": "2026-10-01T09:00:00Z",
		"personnel": []domain.PlanResource{
			{ResourceType: "PERSON", ResourceID: "u-eng-1", ResourceName: "张三"},
			{ResourceType: "PERSON", ResourceID: "u-eng-2", ResourceName: "李四"},
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-1", ResourceName: "频谱仪"},
		},
	}
	if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", EventImplementationPlanned, payload)); err != nil {
		t.Fatalf("applyEvent failed: %v", err)
	}
	if len(repo.events) != 1 || repo.events[0].Notification == nil {
		t.Fatalf("implementation plan must persist one notification, got %+v", repo.events)
	}
	event := *repo.events[0].Notification
	if event.EventType != EventImplementationPlanned {
		t.Fatalf("notification event type = %q", event.EventType)
	}
	if len(event.Recipients) != 2 || event.Recipients[0] != "u-eng-1" || event.Recipients[1] != "u-eng-2" {
		t.Fatalf("recipients must be the plan's people only: %+v", event.Recipients)
	}
}

// 计划完成时间无法解析的服务项会被跳过，不能静默消失在 SLA 口径之外：
// 跳过数量要返回给调用方用于告警。
func TestComputeSlaItemsReportsUnparsablePlannedEnd(t *testing.T) {
	now := time.Now().UTC()
	candidates := []domain.SlaOverdueItem{
		{ID: "SI-OK", ProjectID: "PJ-1", Status: "实施中", PlannedEnd: now.Add(-48 * time.Hour).Format(time.RFC3339), StatusChangedAt: now.Add(-time.Hour)},
		{ID: "SI-BAD", ProjectID: "PJ-1", Status: "实施中", PlannedEnd: "不是时间"},
	}
	items, skipped := computeSlaItems(candidates, nil, now)
	if skipped != 1 {
		t.Fatalf("必须报告被跳过的服务项数量，实际 skipped=%d", skipped)
	}
	if len(items) != 1 || items[0].ID != "SI-OK" {
		t.Fatalf("可解析的服务项仍应产出计划完成超期，实际 %+v", items)
	}
}

// 载荷里没有收件人时（实施准备发起/偏差上报/报告推进），必须回到服务项的当前被指派人：
// 提醒要发给"需要行动的人"，而不是无人可发。
func TestNotificationFallsBackToItemAssignees(t *testing.T) {
	repo := &assigneeRepository{item: domain.ServiceItem{
		TenantID: "t1", ProjectID: "PJ-1", Status: "实施中",
		TeamLeadID: "u-lead", ProjectManagerID: "u-pm", EngineerIDs: []string{"u-e1", " u-e2 "},
	}}
	service := &Service{Repo: repo}
	principal := platform.Principal{TenantID: "t1", UserID: "u-actor"}

	for _, eventType := range []string{EventPreparationStarted, EventDeviationReported, EventReportStatusUpdated} {
		repo.events = nil
		if err := service.applyEvent(context.Background(), deliveryEvent(principal, "PJ-1", "SI-1", eventType, map[string]any{})); err != nil {
			t.Fatalf("%s: applyEvent failed: %v", eventType, err)
		}
		if len(repo.events) != 1 || repo.events[0].Notification == nil {
			t.Fatalf("%s: 必须持久化一条通知，实际 %+v", eventType, repo.events)
		}
		recipients := repo.events[0].Notification.Recipients
		if len(recipients) != 4 || recipients[0] != "u-lead" || recipients[1] != "u-pm" || recipients[2] != "u-e1" || recipients[3] != "u-e2" {
			t.Fatalf("%s: 收件人应为服务项被指派人，实际 %+v", eventType, recipients)
		}
	}
}

// assigneeRepository 让 GetServiceItem 返回带被指派人的服务项，其余能力复用 hookRepository。
type assigneeRepository struct {
	hookRepository
	item domain.ServiceItem
}

func (r *assigneeRepository) GetServiceItem(_ context.Context, _ platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	item := r.item
	item.ID = id
	return item, nil
}

// 定时扫描必须能主动提醒 SLA 超期/临近：此前没有任何周期任务，超期只有人打开页面才可见。
// 幂等键按「服务项 + 口径 + UTC 日期」生成，同一天重复扫描不会重复打扰。
func TestScanSlaNotificationsPublishesToAssignees(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	repo := &assigneeRepository{
		hookRepository: hookRepository{
			overdue: []domain.SlaOverdueItem{{
				ID: "SI-1", ProjectID: "PJ-1", Status: "实施中",
				StatusChangedAt: now.Add(-30 * time.Hour), // 已停留 30 小时
			}},
			rules: []domain.Rule{{Kind: "sla", Enabled: true, Name: "待实施超期", Status: "实施中", DeadlineHours: 10, RemindHours: 2}},
		},
		item: domain.ServiceItem{TenantID: "t1", ProjectID: "PJ-1", TeamLeadID: "u-lead", ProjectManagerID: "u-pm"},
	}
	service := &Service{Repo: repo}

	published, err := service.ScanSlaNotifications(context.Background(), "t1", now)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if published != 1 || len(repo.enqueued) != 1 {
		t.Fatalf("应入队一条超期提醒，实际 published=%d %+v", published, repo.enqueued)
	}
	event := repo.enqueued[0]
	if len(event.Recipients) != 2 || event.Recipients[0] != "u-lead" || event.Recipients[1] != "u-pm" {
		t.Fatalf("收件人应为被指派人，实际 %+v", event.Recipients)
	}
	if event.Priority != "HIGH" {
		t.Fatalf("超期提醒应为高优先级，实际 %q", event.Priority)
	}
	if event.IdempotencyKey != "sla-SI-1-"+domain.SlaKindStatusOverdue+"-2026-09-13" {
		t.Fatalf("幂等键必须按服务项+口径+日期生成，实际 %q", event.IdempotencyKey)
	}
	if event.Scope != platform.NotificationScopeCrossSystem {
		t.Fatalf("必须使用平台白名单 scope，实际 %q", event.Scope)
	}

	// 空租户直接跳过。
	if count, _ := service.ScanSlaNotifications(context.Background(), "  ", now); count != 0 {
		t.Fatalf("空租户不应扫描")
	}
}

// 生产仓储具备 outbox 时，SLA 扫描只负责可靠入队，不能绕过账本直接调用平台通知。
func TestScanSlaNotificationsEnqueuesDurably(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	base := &assigneeRepository{
		hookRepository: hookRepository{
			overdue: []domain.SlaOverdueItem{{
				ID: "SI-OUTBOX", ProjectID: "PJ-1", Status: "实施中",
				StatusChangedAt: now.Add(-30 * time.Hour),
			}},
			rules: []domain.Rule{{Kind: "sla", Enabled: true, Name: "待实施超期", Status: "实施中", DeadlineHours: 10, RemindHours: 2}},
		},
		item: domain.ServiceItem{TenantID: "t1", ProjectID: "PJ-1", TeamLeadID: "u-lead"},
	}
	repo := &slaOutboxRepository{assigneeRepository: base}
	direct := &notificationStub{}
	service := &Service{Repo: repo, Notifications: direct}

	count, err := service.ScanSlaNotifications(context.Background(), "t1", now)
	if err != nil || count != 1 {
		t.Fatalf("durable scan failed: count=%d err=%v", count, err)
	}
	if len(repo.enqueued) != 1 {
		t.Fatalf("SLA notification must be enqueued exactly once, got %+v", repo.enqueued)
	}
	if len(direct.published) != 0 {
		t.Fatalf("outbox-capable repository must not publish directly, got %+v", direct.published)
	}
	if repo.enqueued[0].IdempotencyKey != "sla-SI-OUTBOX-"+domain.SlaKindStatusOverdue+"-2026-09-13" {
		t.Fatalf("unexpected outbox idempotency key %q", repo.enqueued[0].IdempotencyKey)
	}
}

type slaOutboxRepository struct {
	*assigneeRepository
	enqueued []domain.NotificationMessage
}

func (r *slaOutboxRepository) EnqueueNotification(_ context.Context, tenantID string, message domain.NotificationMessage) (bool, error) {
	if tenantID != "t1" {
		return false, errors.New("unexpected tenant")
	}
	r.enqueued = append(r.enqueued, message)
	return true, nil
}

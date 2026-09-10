package application

import (
	"errors"
	"strings"
	"testing"

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

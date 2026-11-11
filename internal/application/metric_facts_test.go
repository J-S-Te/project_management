package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// TestMetricCommandsFailClosedWithoutExplicitPermission 捕获旧读取权限或空范围被扩为写入能力。
// 参数：t 为测试上下文。
// 返回值：无；新事实命令在未获明确动作授权时未拒绝则失败。
func TestMetricCommandsFailClosedWithoutExplicitPermission(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	p := platform.Principal{TenantID: "tenant", UserID: "user", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	if _, err := s.ReviewMetricReport(ctx, p, "item", domain.ReportReviewInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("review: %v", err)
	}
	if _, err := s.PublishMetricCapacity(ctx, p, domain.ResourceCapacityInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("capacity: %v", err)
	}
	if _, err := s.CreateMetricUsage(ctx, p, domain.ResourceUsageInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("usage: %v", err)
	}
	if _, err := s.MutateMetricUsage(ctx, p, "id", "DECISION", domain.ResourceUsageDecision{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("confirm: %v", err)
	}
	p.Permissions["project.person_capacity.manage"] = true
	p.DataScopes = nil
	if _, err := s.PublishMetricCapacity(ctx, p, domain.ResourceCapacityInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty scope: %v", err)
	}
}

// TestMetricDeviceOnlyRecorderCannotRegisterPersonnel 捕获通用登记动作把设备管理员扩为人员工时登记人。
// 参数：t 为测试上下文。
// 返回值：无；缺人员职责仍进入人员登记链时失败。
func TestMetricDeviceOnlyRecorderCannotRegisterPersonnel(t *testing.T) {
	service := &Service{}
	p := platform.Principal{TenantID: "tenant", UserID: "device", Permissions: map[string]bool{"project.resource_usage.record": true, "project.device.manage": true}, DataScopes: []platform.DataScope{{RoleCode: "device_admin", ScopeType: "APPLICATION"}}}
	_, err := service.CreateMetricUsage(context.Background(), p, domain.ResourceUsageInput{ResourceType: "PERSON"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("device-only personnel recording must be forbidden: %v", err)
	}
}

package application

import (
	"context"
	"encoding/json"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"testing"
)

type settlementMetricRefRepo struct{ nativeReadRepository }

// SettlementReferenceServiceItems 返回测试专属合法当前及历史服务项身份。
// 参数：ctx 为请求，tenant/project 为已验证引用。
// 返回值：最小服务项集合，不含人员和文件。
func (r *settlementMetricRefRepo) SettlementReferenceServiceItems(ctx context.Context, tenant, project string) ([]string, error) {
	return []string{"SI-active", "SI-historical"}, nil
}

// TestSettlementReferenceContainsMinimalMetricDimensions 捕获结算无法权威验证服务项和项目组织的问题。
// 参数：t 为测试上下文。
// 返回值：无；最小引用字段缺失或旧语义变化时失败。
func TestSettlementReferenceContainsMinimalMetricDimensions(t *testing.T) {
	repo := &settlementMetricRefRepo{nativeReadRepository{project: domain.Project{ID: "PJ", TenantID: "tenant", ContractID: "C", CustomerID: "customer", OwnerOrgID: "org", ManagerIdentityID: "manager", Version: 1, Status: "实施中"}}}
	service := &Service{Repo: repo}
	p := platform.Principal{TenantID: "tenant", Permissions: map[string]bool{"settlement.project_reference.read": true}}
	ref, err := service.ResolveSettlementProjectReference(context.Background(), p, "PJ")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ref)
	var fields map[string]any
	json.Unmarshal(body, &fields)
	ids, ok := fields["service_item_ids"].([]any)
	if fields["owner_org_id"] != "org" || fields["manager_identity_id"] != "manager" || !ok || len(ids) != 2 || !ref.Active || ref.ContractID != "C" {
		t.Fatalf("missing minimal dimensions %s", body)
	}
}

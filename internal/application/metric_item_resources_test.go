package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type metricItemResourceStub struct {
	Repository
	parentErr                  error
	parentReads, resourceReads int
	itemID                     string
	filter                     platform.ScopeFilter
	page                       int
}

// GetServiceItem 用途：模拟父项授权边界，不提供其他业务实现。
// 参数：ctx 为读取请求，filter 为可信范围，id 为父服务项。
// 返回值：可见父项或预设的范围错误。
func (r *metricItemResourceStub) GetServiceItem(ctx context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	r.parentReads++
	return domain.ServiceItem{ID: id, ProjectID: "project-a"}, r.parentErr
}

// ListItemMetricCapacities 用途：捕获容量读取的父项与原有资源范围。
// 参数：ctx 为请求，filter 为可信范围，item 为已授权父项，page 为页码。
// 返回值：空容量页。
func (r *metricItemResourceStub) ListItemMetricCapacities(ctx context.Context, filter platform.ScopeFilter, item domain.ServiceItem, page int) ([]domain.ResourceCapacity, error) {
	r.resourceReads++
	r.itemID, r.filter, r.page = item.ID, filter, page
	return []domain.ResourceCapacity{}, nil
}

// ListItemMetricUsages 用途：捕获实际用量读取的父项与原有资源范围。
// 参数：ctx 为请求，filter 为可信范围，id 为父项，page 为页码。
// 返回值：空用量页。
func (r *metricItemResourceStub) ListItemMetricUsages(ctx context.Context, filter platform.ScopeFilter, id string, page int) ([]domain.ResourceUsage, error) {
	r.resourceReads++
	r.itemID, r.filter, r.page = id, filter, page
	return []domain.ResourceUsage{}, nil
}

// TestMetricItemResourcesRetainsParentAndResourceScope 用途：验证节点读取保留父项及资源的双重范围。
// 参数：t 为无数据库的应用边界测试。
// 返回值：无；未核对父项或擅自扩大SELF范围时失败。
func TestMetricItemResourcesRetainsParentAndResourceScope(t *testing.T) {
	p := platform.Principal{TenantID: "tenant-a", UserID: "engineer-a", Permissions: map[string]bool{"project.read": true},
		DataScopes: []platform.DataScope{{RoleCode: "engineer", ScopeType: "SELF"}}}
	for _, kind := range []string{"capacities", "usages"} {
		repo := &metricItemResourceStub{}
		_, err := (&Service{Repo: repo}).MetricItemResources(t.Context(), p, "SI-a", kind, 2)
		if err != nil {
			t.Fatal(err)
		}
		if repo.parentReads != 1 || repo.resourceReads != 1 || repo.itemID != "SI-a" ||
			repo.filter.TenantID != "tenant-a" || repo.filter.UserID != "engineer-a" ||
			repo.filter.AllowAll || !repo.filter.AllowSelf || repo.page != 2 {
			t.Fatalf("节点读取扩大范围或丢失父项：%+v", repo)
		}
	}
}

// TestMetricItemResourcesRejectsInvalidOrInvisibleParent 用途：验证无权、非法参数及不可见父项不读取资源。
// 参数：t 为应用边界测试。
// 返回值：无；被拒绝的请求触达资源台账时失败。
func TestMetricItemResourcesRejectsInvalidOrInvisibleParent(t *testing.T) {
	p := platform.Principal{TenantID: "tenant-a", UserID: "reader", Permissions: map[string]bool{"project.read": true},
		DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}}
	for _, input := range []struct {
		id, kind string
		page     int
		denied   bool
	}{
		{"SI-a", "usages", 0, false}, {"SI-a", "usages", 10001, false},
		{"", "usages", 1, false}, {"SI-a", "unknown", 1, false}, {"SI-a", "usages", 1, true},
	} {
		repo := &metricItemResourceStub{}
		if input.denied {
			repo.parentErr = ErrNotFound
		}
		_, err := (&Service{Repo: repo}).MetricItemResources(t.Context(), p, input.id, input.kind, input.page)
		if err == nil || repo.resourceReads != 0 {
			t.Fatalf("非法节点读取未拒绝：%+v %v", input, err)
		}
	}
	repo := &metricItemResourceStub{}
	p.Permissions = map[string]bool{}
	_, err := (&Service{Repo: repo}).MetricItemResources(t.Context(), p, "SI-a", "usages", 1)
	if !errors.Is(err, ErrForbidden) || repo.parentReads != 0 || repo.resourceReads != 0 {
		t.Fatalf("无权读取触达父项：%v %+v", err, repo)
	}
}

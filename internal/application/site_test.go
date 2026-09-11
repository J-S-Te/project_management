package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// siteRepositoryStub 记录站点写入，供权限与校验用例断言。
type siteRepositoryStub struct {
	scopeRepository
	saved []domain.Site
}

func (stub *siteRepositoryStub) ListSites(context.Context, string, string) ([]domain.Site, error) {
	return stub.saved, nil
}
func (stub *siteRepositoryStub) UpsertSite(_ context.Context, item domain.Site) (domain.Site, error) {
	stub.saved = append(stub.saved, item)
	return item, nil
}
func (stub *siteRepositoryStub) DeleteSite(context.Context, string, string) error { return nil }
func (stub *siteRepositoryStub) FindSiteByCode(context.Context, string, string) (domain.Site, error) {
	return domain.Site{}, ErrNotFound
}

// 站点台账的写权限沿用资源主数据权限；只读角色不得修改。
func TestUpsertSiteRequiresResourceManage(t *testing.T) {
	repo := &siteRepositoryStub{}
	service := &Service{Repo: repo}
	principal := principalWith("project.read", platform.DataScope{RoleCode: "engineer", ScopeType: "APPLICATION"})
	if _, err := service.UpsertSite(context.Background(), principal, domain.Site{SiteCode: "S-1", Name: "站点"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("read-only role must not write: %+v", repo.saved)
	}
	if _, err := service.ListSites(context.Background(), principal, ""); err != nil {
		t.Fatalf("project.read must be able to list sites: %v", err)
	}
}

// 坐标是可选采集项，但一旦给出就必须落在合法经纬度范围内：
// 错误的坐标比没有坐标更危险，必须挡在写入前。
func TestUpsertSiteValidatesCoordinates(t *testing.T) {
	repo := &siteRepositoryStub{}
	service := &Service{Repo: repo}
	principal := principalWith("project.resource.manage", platform.DataScope{RoleCode: "device_admin", ScopeType: "APPLICATION"})

	// 未采集坐标：允许保存。
	if _, err := service.UpsertSite(context.Background(), principal, domain.Site{SiteCode: "S-1", Name: "杭州机房"}); err != nil {
		t.Fatalf("site without coordinates must be accepted: %v", err)
	}
	// 合法坐标。
	if _, err := service.UpsertSite(context.Background(), principal, domain.Site{SiteCode: "S-2", Name: "北京机房", HasCoordinates: true, Latitude: 39.9042, Longitude: 116.4074}); err != nil {
		t.Fatalf("valid coordinates rejected: %v", err)
	}
	for _, invalid := range []domain.Site{
		{SiteCode: "S-3", Name: "越界纬度", HasCoordinates: true, Latitude: 91, Longitude: 0},
		{SiteCode: "S-4", Name: "越界经度", HasCoordinates: true, Latitude: 0, Longitude: 181},
		{SiteCode: "", Name: "缺编码"},
		{SiteCode: "S-5", Name: ""},
		{SiteCode: "S-6", Name: "非法状态", Status: "RETIRED"},
	} {
		if _, err := service.UpsertSite(context.Background(), principal, invalid); !errors.Is(err, ErrValidation) {
			t.Fatalf("%+v: err = %v, want ErrValidation", invalid, err)
		}
	}
}

// 坐标为 0,0 是合法位置（几内亚湾），不能与"尚未采集"混为一谈。
func TestSiteZeroCoordinateIsValidWhenMarked(t *testing.T) {
	if err := domain.ValidateSite(domain.Site{SiteCode: "S-1", Name: "赤道站点", HasCoordinates: true, Latitude: 0, Longitude: 0}); err != nil {
		t.Fatalf("0,0 is a legal coordinate: %v", err)
	}
	if err := domain.ValidateSite(domain.Site{SiteCode: "S-1", Name: "未采集"}); err != nil {
		t.Fatalf("missing coordinates must be allowed: %v", err)
	}
}

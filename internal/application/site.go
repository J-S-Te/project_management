package application

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// SiteRepository 是站点台账的持久化边界。与 delivery 一样按需断言，
// 让只实现部分能力的仓储（含测试桩）继续可用。
type SiteRepository interface {
	ListSites(context.Context, string, string) ([]domain.Site, error)
	UpsertSite(context.Context, domain.Site) (domain.Site, error)
	DeleteSite(context.Context, string, string) error
	FindSiteByCode(context.Context, string, string) (domain.Site, error)
}

func (s *Service) siteRepo() (SiteRepository, error) {
	repo, ok := s.Repo.(SiteRepository)
	if !ok {
		return nil, ErrNotFound
	}
	return repo, nil
}

// ListSites 返回站点台账。站点是项目/服务项归属的公共主数据，因此以 project.read 为基线，
// 与人员、设备目录一致：凡是需要渲染站点选择或按站点聚合的角色都要能读。
func (s *Service) ListSites(ctx context.Context, p platform.Principal, status string) ([]domain.Site, error) {
	if err := requireApplicationAuthorization(p, "project.read"); err != nil {
		return nil, err
	}
	repo, err := s.siteRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListSites(ctx, p.TenantID, strings.TrimSpace(status))
}

// UpsertSite 按站点编码幂等写入站点档案。坐标可以由录入人在现场通过浏览器定位自动获取，
// 也可以手工填写；是否提供由调用方决定，服务端只负责校验合法性。
func (s *Service) UpsertSite(ctx context.Context, p platform.Principal, item domain.Site) (domain.Site, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return item, err
	}
	item.TenantID = p.TenantID
	item.SiteCode = strings.TrimSpace(item.SiteCode)
	item.Name = strings.TrimSpace(item.Name)
	item.Address = strings.TrimSpace(item.Address)
	item.Notes = strings.TrimSpace(item.Notes)
	item.Status = strings.ToUpper(strings.TrimSpace(item.Status))
	item.UpdatedAt = p.UserID
	if err := domain.ValidateSite(item); err != nil {
		return item, ValidationError(err.Error())
	}
	repo, err := s.siteRepo()
	if err != nil {
		return item, err
	}
	return repo.UpsertSite(ctx, item)
}

// DeleteSite 停用站点。保留行而不是物理删除：历史服务项仍要能追溯到它的站点。
func (s *Service) DeleteSite(ctx context.Context, p platform.Principal, siteCode string) error {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return err
	}
	if strings.TrimSpace(siteCode) == "" {
		return ValidationError("站点编码不能为空")
	}
	repo, err := s.siteRepo()
	if err != nil {
		return err
	}
	return repo.DeleteSite(ctx, p.TenantID, siteCode)
}

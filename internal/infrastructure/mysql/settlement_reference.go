package mysql

import (
	"context"
	"github.com/j-s-te/project-management/internal/application"
)

// SettlementReferenceServiceItems 返回已验证项目的当前及历史合法服务项最小身份。
// 参数：ctx 为取消上下文，tenant 为签名租户，project 为此前已验证项目。
// 返回值：按ID稳定排序的最多1000个服务项；超限明确失败，不截断成功。
func (r *Repository) SettlementReferenceServiceItems(ctx context.Context, tenant, project string) ([]string, error) {
	ids := []string{}
	err := r.db.WithContext(ctx).Unscoped().Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=?", tenant, project).Order("id").Limit(1001).Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) > 1000 {
		return nil, application.ErrProjectSnapshotLimit
	}
	return ids, nil
}

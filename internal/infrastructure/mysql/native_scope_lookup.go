package mysql

import (
	"context"
	"encoding/json"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

// ReadNativeScopeChange 精确读取原生项目的指定提案，包括已处理的历史提案。
// 参数：ctx/filter 为授权读取范围，projectID/changeID 不可跨项目组合。
// 返回值：一致的当前范围与至多一个目标提案；旧链接不会回退到最新提案。
func (r *Repository) ReadNativeScopeChange(
	ctx context.Context, filter platform.ScopeFilter, projectID, changeID string,
) (domain.NativeScopeState, error) {
	return r.readNativeScope(ctx, filter, projectID, changeID)
}

// readNativeScope 在同一事务读取项目生效范围及历史或精确提案。
// 参数：ctx/filter 为读取范围，projectID 为项目，changeID 为空时返回最近二十条。
// 返回值：真实范围状态及相应提案，项目无权或数据损坏时返回错误。
func (r *Repository) readNativeScope(
	ctx context.Context, filter platform.ScopeFilter, projectID, changeID string,
) (domain.NativeScopeState, error) {
	state := domain.NativeScopeState{Changes: []domain.NativeScopeChange{}}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		project, err := lockNativeProject(tx, filter, projectID)
		if err != nil {
			return err
		}
		var scope nativeScopeRecord
		projectScope := "tenant_id=? AND project_id=?"
		if err = tx.Where(projectScope+" AND version=?", filter.TenantID, projectID, project.ScopeVersion).
			Take(&scope).Error; err != nil {
			return err
		}
		state.ProjectID, state.ScopeVersion, state.ProjectVersion = projectID, project.ScopeVersion, project.Version
		if err = json.Unmarshal(scope.ServiceItems, &state.ServiceItems); err != nil {
			return err
		}
		var rows []nativeScopeChangeRecord
		query := tx.Where(projectScope, filter.TenantID, projectID)
		if changeID == "" {
			query = query.Order("created_at DESC,id DESC").Limit(20)
		} else {
			query = query.Where("id=?", changeID).Limit(1)
		}
		if err = query.Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			change, err := scopeChangeDomain(row)
			if err != nil {
				return err
			}
			state.Changes = append(state.Changes, change)
		}
		return readNativeScopeApplicants(tx, filter.TenantID, state.Changes)
	})
	return state, err
}

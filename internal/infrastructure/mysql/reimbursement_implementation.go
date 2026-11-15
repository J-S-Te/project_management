package mysql

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

type expenseImplementationCursor struct {
	ServiceItemID string `json:"service_item_id"`
	ResourceID    string `json:"resource_id"`
}

// expensePlanPersonMatch 精确匹配人员类型与资源编号，兼容历史计划且不按名称授权。
// 参数：无；表别名 ip/c 分别表示当前计划和同租户资质档案。
// 返回值：仅含固定结构的 JSON 匹配条件，SQLite 分支供隔离仓储测试使用。
func (r *Repository) expensePlanPersonMatch() string {
	if r.db.Dialector.Name() == "sqlite" {
		return `EXISTS (SELECT 1 FROM json_each(ip.personnel) person
 WHERE json_extract(person.value,'$.resource_type')='PERSON'
 AND json_extract(person.value,'$.resource_id')=c.resource_id)`
	}
	return `JSON_CONTAINS(ip.personnel,
 JSON_OBJECT('resource_type','PERSON','resource_id',c.resource_id))`
}

// expenseImplementationQuery 从当前有效服务项的实施计划解析不可改绑的平台身份。
// 参数：ctx 控制查询；tenant/project 为精确租户与项目边界。
// 返回值：尚未执行的最小关联查询；不根据资质启停追溯撤销既有计划人员。
func (r *Repository) expenseImplementationQuery(ctx context.Context, tenant, project string) *gorm.DB {
	return r.db.WithContext(ctx).Table("pm_service_item s").
		Joins("JOIN pm_impl_plan ip ON ip.tenant_id=s.tenant_id AND ip.service_item_id=s.id").
		Joins(`JOIN pm_capability c ON c.tenant_id=s.tenant_id AND c.resource_type='PERSON'
 AND c.user_id<>'' AND c.user_id=TRIM(c.user_id) AND `+r.expensePlanPersonMatch()).
		Where("s.tenant_id=? AND s.project_id=? AND s.archived_at IS NULL", tenant, project)
}

// ReadExpenseImplementationPersonnel 读取指定服务项当前实施人员的稳定平台身份。
// 参数：ctx 为上下文；tenant/project/item 为完整来源边界。
// 返回值：同租户档案已关联主体的人员；无计划返回空列表，读取失败返回错误。
func (r *Repository) ReadExpenseImplementationPersonnel(
	ctx context.Context, tenant, project, item string,
) ([]domain.PlanResource, error) {
	var rows []struct{ ResourceID, ResourceName, UserID string }
	err := r.expenseImplementationQuery(ctx, tenant, project).
		Where("s.id=?", item).
		Distinct("c.resource_id, c.resource_name, c.user_id").
		Order("c.resource_id").Scan(&rows).Error
	people := make([]domain.PlanResource, 0, len(rows))
	for _, row := range rows {
		people = append(people, domain.PlanResource{ResourceType: "PERSON",
			ResourceID: row.ResourceID, ResourceName: row.ResourceName, UserID: row.UserID})
	}
	return people, err
}

// ListExpenseImplementationParticipants 分页读取当前项目的默认实施人员资格。
// 参数：ctx/scope 为当前授权，project 为项目，cursor/limit 为独立分页边界。
// 返回值：只读人员及其服务项；越界、非法游标或存储失败返回错误。
func (r *Repository) ListExpenseImplementationParticipants(
	ctx context.Context, scope platform.ScopeFilter, project, cursor string, limit int,
) (domain.ExpenseImplementationParticipantPage, error) {
	out := domain.ExpenseImplementationParticipantPage{
		Items: []domain.ExpenseImplementationParticipant{},
	}
	after, err := decodeExpenseImplementationCursor(cursor)
	if err != nil || limit < 1 || limit > 100 {
		return out, application.ErrValidation
	}
	if _, err := r.GetProject(ctx, scope, project); err != nil {
		return out, err
	}
	query := r.expenseImplementationQuery(ctx, scope.TenantID, project)
	if scope.AssignedItemsOnly {
		items := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}),
			r.db.WithContext(ctx), scope).Select("pm_service_item.id")
		query = query.Where("s.id IN (?)", items)
	}
	err = query.
		Where("s.id>? OR (s.id=? AND c.resource_id>?)",
			after.ServiceItemID, after.ServiceItemID, after.ResourceID).
		Distinct("s.id AS service_item_id, c.resource_id, c.user_id AS subject_id, c.resource_name AS display_name").
		Order("s.id,c.resource_id").Limit(limit + 1).Scan(&out.Items).Error
	if err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[len(out.Items)-1]
		payload, _ := json.Marshal(expenseImplementationCursor{last.ServiceItemID, last.ResourceID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(payload)
	}
	return out, nil
}

// decodeExpenseImplementationCursor 解析有界的服务项及人员资源复合游标。
// 参数：cursor 为空表示第一页，其余必须是完整的上一页边界。
// 返回值：解析后的边界；非法编码、空标识或超长输入返回参数错误。
func decodeExpenseImplementationCursor(cursor string) (expenseImplementationCursor, error) {
	var after expenseImplementationCursor
	if cursor == "" {
		return after, nil
	}
	if len(cursor) > 512 {
		return after, application.ErrValidation
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(payload, &after) != nil ||
		after.ServiceItemID == "" || len(after.ServiceItemID) > 128 ||
		after.ResourceID == "" || len(after.ResourceID) > 128 {
		return after, application.ErrValidation
	}
	return after, nil
}

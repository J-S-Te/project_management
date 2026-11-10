package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ExpenseCandidates 返回有界最小标识候选，由应用统一算法决定实际资格。
// 参数：ctx 控制读取，tenant/actor/keyword 为来源、人员和关键词，project/item 为复合游标，limit 为上限。
// 返回值：不含敏感字段的项目和有效服务项标识；查询失败返回错误。
func (r *Repository) ExpenseCandidates(ctx context.Context, tenant, actor, keyword, project, item string, limit int) ([]application.ExpenseCandidate, error) {
	actorJSON, _ := json.Marshal(actor)
	responsibility := `(p.owner_identity_id=? OR p.manager_identity_id=? OR EXISTS (SELECT 1 FROM pm_expense_participant ep WHERE ep.tenant_id=p.tenant_id AND ep.project_id=p.id AND ep.subject_id=? AND ep.status='ACTIVE' AND ep.valid_from<=? AND ep.valid_until>?))`
	query := `SELECT project_id,service_item_id FROM (
 SELECT p.id AS project_id,'' AS service_item_id,p.name FROM pm_project p WHERE p.tenant_id=? AND ` + responsibility + `
 UNION ALL SELECT p.id AS project_id,s.id AS service_item_id,p.name FROM pm_project p JOIN pm_service_item s ON s.project_id=p.id AND s.tenant_id=p.tenant_id WHERE p.tenant_id=? AND s.archived_at IS NULL AND (` + responsibility + ` OR s.team_lead_id=? OR s.project_manager_id=? OR INSTR(s.engineer_ids,?)>0)
 ) candidates WHERE name LIKE ? AND (project_id>? OR (project_id=? AND service_item_id>?)) ORDER BY project_id,service_item_id LIMIT ?`
	now := time.Now().UTC()
	args := []any{tenant, actor, actor, actor, now, now, tenant, actor, actor, actor, now, now, actor, actor, string(actorJSON), "%" + keyword + "%", project, project, item, limit}
	rows := []application.ExpenseCandidate{}
	err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error
	return rows, err
}

// expenseParticipantDomain 将数据库资格记录映射到最小管理与领域结构。
// 参数：row 为持久化资格。
// 返回值：完整版本及审计信息，不附带平台人员敏感字段。
func expenseParticipantDomain(row expenseParticipantRow) domain.ExpenseParticipant {
	return domain.ExpenseParticipant{TenantID: row.TenantID, ProjectID: row.ProjectID, SubjectID: row.SubjectID, Status: row.Status, ValidFrom: row.ValidFrom, ValidUntil: row.ValidUntil, GrantedBy: row.GrantedBy, Reason: row.Reason, Version: row.Version}
}

// ReadExpenseParticipant 读取同租户同项目指定主体资格，缺失视为无显式资格。
// 参数：ctx 控制查询，tenant/project/actor 为精确稳定标识。
// 返回值：存在时返回资格；缺失返回 nil；数据库故障返回错误。
func (r *Repository) ReadExpenseParticipant(ctx context.Context, tenant, project, actor string) (*domain.ExpenseParticipant, error) {
	var row expenseParticipantRow
	err := r.db.WithContext(ctx).Table("pm_expense_participant").Where("tenant_id=? AND project_id=? AND subject_id=?", tenant, project, actor).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	grant := expenseParticipantDomain(row)
	return &grant, nil
}

// ListExpenseParticipants 在当前项目管理范围分页读取显式资格名单。
// 参数：ctx 为上下文，scope 为可信范围，project 为项目，after/limit 为分页边界。
// 返回值：限定条数的资格记录；范围无效或数据库失败返回错误。
func (r *Repository) ListExpenseParticipants(ctx context.Context, scope platform.ScopeFilter, project, after string, limit int) ([]domain.ExpenseParticipant, error) {
	if _, err := r.GetProject(ctx, scope, project); err != nil {
		return nil, err
	}
	var rows []expenseParticipantRow
	err := r.db.WithContext(ctx).Table("pm_expense_participant").Where("tenant_id=? AND project_id=? AND subject_id>?", scope.TenantID, project, after).Order("subject_id").Limit(limit).Find(&rows).Error
	out := []domain.ExpenseParticipant{}
	for _, row := range rows {
		out = append(out, expenseParticipantDomain(row))
	}
	return out, err
}

// SaveExpenseParticipant 在项目锁和版本条件下写名单，并将审计同事务落库。
// 参数：ctx 为上下文，scope 为可信范围，in 为资格命令，expected 为期望资格版本。
// 返回值：新版本资格；并发、范围或审计失败时全部回滚。
func (r *Repository) SaveExpenseParticipant(ctx context.Context, scope platform.ScopeFilter, in domain.ExpenseParticipant, expected uint64) (domain.ExpenseParticipant, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project projectRecord
		if err := applyProjectScope(tx.Model(&projectRecord{}), scope, "pm_project").Where("pm_project.id=?", in.ProjectID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&project).Error; err != nil {
			return mapNotFound(err)
		}
		var old expenseParticipantRow
		query := tx.Table("pm_expense_participant").Where("tenant_id=? AND project_id=? AND subject_id=?", scope.TenantID, in.ProjectID, in.SubjectID)
		err := query.Take(&old).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if old.Version != expected {
			return application.ErrConflict
		}
		in.Version = old.Version + 1
		row := expenseParticipantRow{TenantID: scope.TenantID, ProjectID: in.ProjectID, SubjectID: in.SubjectID, Status: in.Status, ValidFrom: in.ValidFrom, ValidUntil: in.ValidUntil, GrantedBy: in.GrantedBy, Reason: in.Reason, Version: in.Version}
		if in.Status == "REVOKED" {
			if old.Version == 0 {
				return application.ErrNotFound
			}
			row.ValidFrom = old.ValidFrom
			row.ValidUntil = old.ValidUntil
			in.ValidFrom = old.ValidFrom
			in.ValidUntil = old.ValidUntil
		}
		if err := tx.Table("pm_expense_participant").Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		return tx.Create(&auditOutboxRecord{ID: ulid.Make().String(), TenantID: scope.TenantID, EventID: ulid.Make().String(), ActorID: actorPointer(in.GrantedBy), ActorType: "USER", Action: "PROJECT_MANAGEMENT:EXPENSE_PARTICIPANT_CHANGED", ResourceType: "project", ResourceID: in.ProjectID, Result: "SUCCESS", DeliveryStatus: "pending", NextAttemptAt: now, CreatedAt: now}).Error
	})
	return in, err
}

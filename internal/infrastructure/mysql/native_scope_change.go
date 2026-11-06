package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type nativeScopeChangeRecord struct {
	CatalogHash            string  `gorm:"size:64;not null"`
	ID                     string  `gorm:"primaryKey;size:32"`
	TenantID               string  `gorm:"size:64;not null;uniqueIndex:uq_native_scope_operation,priority:1;uniqueIndex:uq_native_scope_pending,priority:1"`
	ProjectID              string  `gorm:"size:32;not null;uniqueIndex:uq_native_scope_operation,priority:2;index"`
	OperationID            string  `gorm:"size:64;not null;uniqueIndex:uq_native_scope_operation,priority:3"`
	PendingProject         *string `gorm:"size:32;uniqueIndex:uq_native_scope_pending,priority:2"`
	Fingerprint            string  `gorm:"size:64;not null"`
	BaseScopeVersion       uint64
	ExpectedProjectVersion uint64
	Version                uint64
	Status                 string `gorm:"size:16;not null"`
	Reason                 string `gorm:"size:500;not null"`
	RequestedBy            string `gorm:"size:128;not null"`
	DecidedBy              string `gorm:"size:128;not null"`
	DecisionOperation      string `gorm:"size:64;not null"`
	DecisionFingerprint    string `gorm:"size:64;not null"`
	Comment                string `gorm:"size:500;not null"`
	ScopeHash              string `gorm:"size:64;not null"`
	AppliedScopeVersion    uint64
	ServiceItems           []byte `gorm:"type:json;not null"`
	CreatedAt              time.Time
	DecidedAt              *time.Time
}

// TableName 返回原生范围审批聚合表名。
// 参数：无。
// 返回值：固定表名。
func (nativeScopeChangeRecord) TableName() string { return "pm_native_scope_change" }

// scopeChangeDomain 将持久聚合还原为受控API内容。
// 参数：row 为数据库真实提案。
// 返回值：包含冻结范围的提案，损坏JSON返回错误。
func scopeChangeDomain(row nativeScopeChangeRecord) (domain.NativeScopeChange, error) {
	result := domain.NativeScopeChange{ID: row.ID, ProjectID: row.ProjectID, OperationID: row.OperationID, BaseScopeVersion: row.BaseScopeVersion, ExpectedProjectVersion: row.ExpectedProjectVersion, Version: row.Version, Status: row.Status, Reason: row.Reason, RequestedBy: row.RequestedBy, DecidedBy: row.DecidedBy, DecisionOperationID: row.DecisionOperation, Comment: row.Comment, ScopeHash: row.ScopeHash, AppliedScopeVersion: row.AppliedScopeVersion, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt}
	err := json.Unmarshal(row.ServiceItems, &result.ServiceItems)
	return result, err
}

// lockNativeProject 在当前授权范围内锁定唯一原生项目头。
// 参数：tx 为事务，filter 为可信数据范围，id 为项目主键。
// 返回值：项目记录；合同或未知来源及缺失主键均拒绝。
func lockNativeProject(tx *gorm.DB, filter platform.ScopeFilter, id string) (projectRecord, error) {
	var project projectRecord
	err := applyProjectScope(tx.Model(&projectRecord{}), filter, "pm_project").Clauses(clause.Locking{Strength: "UPDATE"}).Where("pm_project.id=?", id).Take(&project).Error
	if err != nil {
		return project, mapNotFound(err)
	}
	if project.SourceKind != "NATIVE" || project.ScopeVersion < 1 {
		return project, application.PreconditionError("该项目不适用原生范围审批")
	}
	return project, nil
}

// ReadNativeScope 一致读取项目头、不可变当前范围及最近二十个提案。
// 参数：ctx/filter 为当前授权，id 为项目主键。
// 返回值：范围状态；不将损坏基线或合同来源当成本地事实。
func (r *Repository) ReadNativeScope(ctx context.Context, filter platform.ScopeFilter, id string) (domain.NativeScopeState, error) {
	state := domain.NativeScopeState{Changes: []domain.NativeScopeChange{}}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		project, err := lockNativeProject(tx, filter, id)
		if err != nil {
			return err
		}
		var scope nativeScopeRecord
		if err = tx.Where("tenant_id=? AND project_id=? AND version=?", filter.TenantID, id, project.ScopeVersion).Take(&scope).Error; err != nil {
			return err
		}
		state.ProjectID = id
		state.ScopeVersion = project.ScopeVersion
		state.ProjectVersion = project.Version
		if err = json.Unmarshal(scope.ServiceItems, &state.ServiceItems); err != nil {
			return err
		}
		var rows []nativeScopeChangeRecord
		if err = tx.Where("tenant_id=? AND project_id=?", filter.TenantID, id).Order("created_at DESC,id DESC").Limit(20).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			change, err := scopeChangeDomain(row)
			if err != nil {
				return err
			}
			state.Changes = append(state.Changes, change)
		}
		return nil
	})
	return state, err
}

// RequestNativeScope 将待审批范围与审计一起提交而不覆盖当前版本。
// 参数：ctx/filter 为请求授权，id/actor 为可信坐标，in/items 为已规范化提案。
// 返回值：新提案或精确重放；并发待审批、载荷变更和非未开工阶段拒绝。
func (r *Repository) RequestNativeScope(ctx context.Context, filter platform.ScopeFilter, id, actor string, in domain.NativeScopeInput, items []domain.ServiceItem) (domain.NativeScopeChange, error) {
	var result domain.NativeScopeChange
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		project, err := lockNativeProject(tx, filter, id)
		if err != nil {
			return err
		}
		fingerprint, err := nativeScopeCommandFingerprint(actor, in, items)
		if err != nil {
			return err
		}
		var old nativeScopeChangeRecord
		err = tx.Where("tenant_id=? AND project_id=? AND operation_id=?", filter.TenantID, id, in.OperationID).Take(&old).Error
		if err == nil {
			if old.Fingerprint != fingerprint {
				return application.ErrConflict
			}
			result, err = scopeChangeDomain(old)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if project.Version != in.ExpectedProjectVersion || project.ScopeVersion != in.BaseScopeVersion {
			return application.ErrConflict
		}
		if err = ensureNoPendingNativeScope(tx, project); err != nil {
			return err
		}
		active, err := lockNativeScopeEligible(tx, project)
		if err != nil {
			return err
		}
		if err = checkNativeScopeBaseline(tx, project, active); err != nil {
			return err
		}
		if err = validateNativeScopeCatalog(tx, filter.TenantID, items); err != nil {
			return err
		}
		row, err := newNativeScopeChange(project, actor, in, items, active, fingerprint)
		if err != nil {
			return err
		}
		row.CatalogHash, err = nativeScopeCatalogFingerprint(tx, filter.TenantID, items)
		if err != nil {
			return err
		}
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		if err = saveNativeScopeEvent(ctx, tx, project, row, "NATIVE_SCOPE_CHANGE_REQUESTED", actor); err != nil {
			return err
		}
		result, err = scopeChangeDomain(row)
		return err
	})
	return result, err
}

// nativeScopeCommandFingerprint 固定操作号对应的规范载荷与真实发起人。
// 参数：actor 为身份，in 为原请求，items 为目录规范化范围。
// 返回值：完整请求摘要或序列化错误。
func nativeScopeCommandFingerprint(actor string, in domain.NativeScopeInput, items []domain.ServiceItem) (string, error) {
	raw, err := json.Marshal(struct {
		Actor string
		Input domain.NativeScopeInput
		Items []domain.ServiceItem
	}{actor, in, items})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

// newNativeScopeChange 为新增行分配稳定本地身份并冻结规范范围。
// 参数：project/actor 为真实身份，in/items 为提案，active 为现行范围，fingerprint 为请求摘要。
// 返回值：待审批聚合；引用非现行行号时拒绝。
func newNativeScopeChange(project projectRecord, actor string, in domain.NativeScopeInput, items []domain.ServiceItem, active []serviceItemRecord, fingerprint string) (nativeScopeChangeRecord, error) {
	row := nativeScopeChangeRecord{ID: ulid.Make().String(), TenantID: project.TenantID, ProjectID: project.ID, OperationID: in.OperationID, PendingProject: &project.ID, Fingerprint: fingerprint, BaseScopeVersion: in.BaseScopeVersion, ExpectedProjectVersion: in.ExpectedProjectVersion, Version: 1, Status: "PENDING", Reason: in.Reason, RequestedBy: actor, CreatedAt: time.Now().UTC()}
	known := map[string]bool{}
	for _, item := range active {
		known[item.SourceServiceID] = true
	}
	seen := map[string]bool{}
	for index := range items {
		line := items[index].SourceServiceID
		if line != "" && !known[line] {
			return row, application.ValidationError("只能引用当前范围的稳定行标识")
		}
		if line == "" {
			line = fmt.Sprintf("NL-%s-%03d", row.ID, index+1)
		}
		if seen[line] {
			return row, application.ErrValidation
		}
		seen[line] = true
		items[index].SourceServiceID = line
		items[index].TenantID = project.TenantID
		items[index].ProjectID = project.ID
	}
	row.ScopeHash = application.NativeScopeFingerprint(items)
	var err error
	row.ServiceItems, err = json.Marshal(items)
	return row, err
}

// DecideNativeScope 以数据库事务执行异人决策和不可变版本生效。
// 参数：ctx/filter 为实时授权，projectID/id/actor 为坐标，in 为精确决策。
// 返回值：真实最终回执；任何校验或审计失败整体回滚。
func (r *Repository) DecideNativeScope(ctx context.Context, filter platform.ScopeFilter, projectID, id, actor string, in domain.NativeScopeDecision) (domain.NativeScopeChange, error) {
	var result domain.NativeScopeChange
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		project, err := lockNativeProject(tx, filter, projectID)
		if err != nil {
			return err
		}
		var row nativeScopeChangeRecord
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND project_id=? AND id=?", filter.TenantID, projectID, id).Take(&row).Error; err != nil {
			return mapNotFound(err)
		}
		fingerprint, _ := nativeScopeCommandFingerprint(actor, domain.NativeScopeInput{OperationID: in.OperationID, Reason: in.Decision + "\n" + in.Comment, BaseScopeVersion: in.ExpectedVersion}, nil)
		if row.Status != "PENDING" {
			if row.DecisionOperation != in.OperationID || row.DecisionFingerprint != fingerprint {
				return application.ErrConflict
			}
			result, err = scopeChangeDomain(row)
			return err
		}
		if row.Version != in.ExpectedVersion {
			return application.ErrConflict
		}
		if in.Decision == "WITHDRAW" {
			if row.RequestedBy != actor {
				return application.ErrForbidden
			}
		} else if row.RequestedBy == actor {
			return application.ErrForbidden
		}
		status := map[string]string{"APPROVE": "APPROVED", "REJECT": "REJECTED", "WITHDRAW": "WITHDRAWN"}[in.Decision]
		if status == "" {
			return application.ErrValidation
		}
		if in.Decision == "APPROVE" {
			if err = applyNativeScope(tx, &project, &row, actor); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		row.Status = status
		row.Version++
		row.PendingProject = nil
		row.DecidedBy = actor
		row.DecidedAt = &now
		row.Comment = in.Comment
		row.DecisionOperation = in.OperationID
		row.DecisionFingerprint = fingerprint
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
		if err = saveNativeScopeEvent(ctx, tx, project, row, "NATIVE_SCOPE_CHANGE_"+status, actor); err != nil {
			return err
		}
		result, err = scopeChangeDomain(row)
		return err
	})
	return result, err
}

// ensureNoPendingNativeScope 阻止审批中的项目产生履约写入。
// 参数：tx 为持有项目头锁的事务，project 为当前项目。
// 返回值：合同保持原行为；本地待审批明确返回前置错误。
func ensureNoPendingNativeScope(tx *gorm.DB, project projectRecord) error {
	if project.SourceKind != "NATIVE" {
		return nil
	}
	var pending nativeScopeChangeRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND pending_project=?", project.TenantID, project.ID).Take(&pending).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return application.PreconditionError("原生范围变更待审批，请先批准、驳回或撤回")
}

// applyNativeScope 在当前项目和提案锁内创建下一版待确认范围。
// 参数：tx 为事务，project/row 为锁定聚合，actor 为真实批准人。
// 返回值：失败整体回滚；不触发合同补充或报告状态机。
func applyNativeScope(tx *gorm.DB, project *projectRecord, row *nativeScopeChangeRecord, actor string) error {
	if project.ScopeVersion != row.BaseScopeVersion || project.Version != row.ExpectedProjectVersion {
		return application.ErrConflict
	}
	active, err := lockNativeScopeEligible(tx, *project)
	if err != nil {
		return err
	}
	if err = checkNativeScopeBaseline(tx, *project, active); err != nil {
		return err
	}
	var items []domain.ServiceItem
	if err = json.Unmarshal(row.ServiceItems, &items); err != nil {
		return err
	}
	if application.NativeScopeFingerprint(items) != row.ScopeHash {
		return application.ErrConflict
	}
	if err = validateNativeScopeCatalog(tx, project.TenantID, items); err != nil {
		return err
	}
	catalogHash, err := nativeScopeCatalogFingerprint(tx, project.TenantID, items)
	if err != nil {
		return err
	}
	if catalogHash != row.CatalogHash {
		return application.ErrConflict
	}
	now := time.Now().UTC()
	if err = tx.Where("tenant_id=? AND project_id=?", project.TenantID, project.ID).Delete(&serviceItemRecord{}).Error; err != nil {
		return err
	}
	for index := range items {
		items[index].ID = ulid.Make().String()
		items[index].Status = domain.ServiceItemStatusPendingConfirm
		item := items[index]
		record := serviceItemRecord{ID: item.ID, TenantID: project.TenantID, ProjectID: project.ID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, Special: item.Special, TestMode: item.TestMode, RequiredCodes: jsonValue(item.RequiredCodes), Status: item.Status, ConflictStatus: "UNCHECKED", Version: 1, StatusChangedAt: &now, CreatedAt: now, UpdatedAt: now, UpdatedBy: actor}
		if err = tx.Create(&record).Error; err != nil {
			return err
		}
	}
	payload, err := json.Marshal(items)
	if err != nil {
		return err
	}
	next := project.ScopeVersion + 1
	if err = tx.Create(&nativeScopeRecord{TenantID: project.TenantID, ProjectID: project.ID, Version: next, SourceID: project.SourceID, ActorIdentityID: actor, ServiceItems: payload, CreatedAt: now}).Error; err != nil {
		return err
	}
	if err = tx.Model(project).Updates(map[string]any{"scope_version": next, "version": project.Version + 1, "services": len(items), "status": domain.ProjectStatusPendingDecomposition, "progress": 0, "updated_at": now}).Error; err != nil {
		return err
	}
	row.AppliedScopeVersion = next
	row.ServiceItems = payload
	return nil
}

// ReplayNativeScope 在读取可变目录前恢复相同请求的持久回执。
// 参数：ctx/filter 为当前授权，id/actor 为可信坐标，in 为原冻结请求。
// 返回值：精确重放结果与存在标记；同号换载荷仍冲突，不重新解释目录。
func (r *Repository) ReplayNativeScope(ctx context.Context, filter platform.ScopeFilter, id, actor string, in domain.NativeScopeInput) (domain.NativeScopeChange, bool, error) {
	var result domain.NativeScopeChange
	found := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockNativeProject(tx, filter, id); err != nil {
			return err
		}
		var row nativeScopeChangeRecord
		err := tx.Where("tenant_id=? AND project_id=? AND operation_id=?", filter.TenantID, id, in.OperationID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		var items []domain.ServiceItem
		if err = json.Unmarshal(row.ServiceItems, &items); err != nil {
			return err
		}
		if len(in.ServiceItems) != len(items) {
			return application.ErrConflict
		}
		for index := range items {
			items[index].ID = ""
			items[index].ProjectID = ""
			items[index].TenantID = ""
			items[index].SourceServiceID = strings.TrimSpace(in.ServiceItems[index].SourceID)
		}
		fingerprint, err := nativeScopeCommandFingerprint(actor, in, items)
		if err != nil {
			return err
		}
		if fingerprint != row.Fingerprint {
			return application.ErrConflict
		}
		result, err = scopeChangeDomain(row)
		return err
	})
	return result, found, err
}

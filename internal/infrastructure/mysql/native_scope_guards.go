package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/auditctx"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// lockNativeScopeEligible 限制整版替换仅作用于没有实施或报告证据的待确认范围。
// 参数：tx 为项目头锁内事务，project 为当前原生项目。
// 返回值：已锁当前服务项；受控退回以外的状态及历史实施事实明确拒绝。
func lockNativeScopeEligible(tx *gorm.DB, project projectRecord) ([]serviceItemRecord, error) {
	var rows []serviceItemRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND project_id=?", project.TenantID, project.ID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, application.ErrPrecondition
	}
	ids := make([]string, 0, len(rows))
	for _, item := range rows {
		if (item.Status != "待确认" && item.Status != "待复核") || item.TeamLeadID != "" || item.ProjectManagerID != "" || len(decodeStrings(item.EngineerIDs)) > 0 || len(decodeStrings(item.EquipmentIDs)) > 0 || item.PlannedStart != nil || item.PlannedEnd != nil || item.ReportRevision > 0 || (item.ReportStatus != "" && item.ReportStatus != "NONE") {
			return nil, application.PreconditionError("范围变更只支持未开工且已按原流程退回待确认的服务项")
		}
		ids = append(ids, item.ID)
	}
	var count int64
	for _, model := range []any{&reportRevisionRecord{}, &evidenceFileRecord{}} {
		if err := tx.Model(model).Where("tenant_id=? AND service_item_id IN ?", project.TenantID, ids).Count(&count).Error; err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, application.PreconditionError("已有报告或现场证据，不能整版替换范围")
		}
	}
	started := []string{application.EventFieldStarted, application.EventFieldCheckedIn, application.EventFieldRecordSubmitted, application.EventFieldSignatureCaptured, application.EventFieldCompleted, application.EventReportStatusUpdated}
	if err := tx.Model(&deliveryEventRecord{}).Where("tenant_id=? AND project_id=? AND event_type IN ?", project.TenantID, project.ID, started).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, application.PreconditionError("项目已有实施或报告历史，不适用首批范围变更")
	}
	if err := tx.Model(&implPlanRecord{}).Where("tenant_id=? AND service_item_id IN ? AND (planned_start IS NOT NULL OR planned_end IS NOT NULL)", project.TenantID, ids).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, application.ErrPrecondition
	}
	return rows, nil
}

// decodeStrings 还原服务项已有人员或设备身份数组。
// 参数：data 为库内JSON数组。
// 返回值：损坏数组返回非空哨兵使未开工校验拒绝。
func decodeStrings(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var result []string
	if json.Unmarshal(data, &result) != nil {
		return []string{"INVALID"}
	}
	return result
}

// checkNativeScopeBaseline 在确认及批准事务内核验完整现行权威范围。
// 参数：tx 为项目锁内事务，project 为头版本，active 为当前未归档项。
// 返回值：完整范围相同才允许；不把本地差异送入合同补充流程。
func checkNativeScopeBaseline(tx *gorm.DB, project projectRecord, active []serviceItemRecord) error {
	var baseline nativeScopeRecord
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND project_id=? AND version=?", project.TenantID, project.ID, project.ScopeVersion).Take(&baseline).Error; err != nil {
		return err
	}
	var items []domain.ServiceItem
	if err := json.Unmarshal(baseline.ServiceItems, &items); err != nil {
		return err
	}
	current := make([]domain.ServiceItem, 0, len(active))
	for _, row := range active {
		current = append(current, serviceFromRecord(row))
	}
	if application.NativeScopeFingerprint(items) != application.NativeScopeFingerprint(current) {
		return application.PreconditionError("服务范围与本地生效版本不一致，请通过原生范围审批处理")
	}
	return nil
}

// validateNativeScopeCatalog 在决策事务内重读目录和站点权威并校验冻结内容。
// 参数：tx 为事务，tenant 为租户，items 为规范范围。
// 返回值：目录启停、标准、能力或站点漂移均返回冲突。
func validateNativeScopeCatalog(tx *gorm.DB, tenant string, items []domain.ServiceItem) error {
	if len(items) < 1 || len(items) > 200 {
		return application.ErrValidation
	}
	for _, item := range items {
		var category detectionCategoryRecord
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND category=? AND enabled=?", tenant, item.Category, true).Take(&category).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return application.ErrConflict
		}
		if err != nil {
			return err
		}
		codes := domain.SplitCapabilityCodes(category.RequiredCodes)
		for index := range codes {
			codes[index] = strings.ToUpper(strings.TrimSpace(codes[index]))
		}
		sort.Strings(codes)
		codes = slices.Compact(codes)
		required := append([]string{}, item.RequiredCodes...)
		sort.Strings(required)
		if !slices.Equal(codes, required) || category.SystemStandard != item.SystemStandard || (category.SpecialMethod == domain.SpecialMethodRequired && item.TestMode != "PENETRATION") || (item.TestMode != "STANDARD" && item.TestMode != "PENETRATION") {
			return application.ErrConflict
		}
		if item.SiteCode != "" {
			var site siteRecord
			err = tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND site_code=? AND status='ACTIVE'", tenant, item.SiteCode).Take(&site).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return application.ErrConflict
			}
			if err != nil {
				return err
			}
			if site.Name != item.Site {
				return application.ErrConflict
			}
		}
	}
	return nil
}

// guardNativeScopeConfirmation 在原确认事务中串行化原生范围审批并验证完整版本。
// 参数：tx 为事务，tenant/project 为当前服务项坐标。
// 返回值：合同不增加业务门禁，本地待审批或范围漂移明确拒绝。
func guardNativeScopeConfirmation(tx *gorm.DB, tenant, projectID string) error {
	var project projectRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenant, projectID).Take(&project).Error; err != nil {
		return err
	}
	if project.SourceKind == "" || project.SourceKind == "CONTRACT" {
		return nil
	}
	if project.SourceKind != "NATIVE" {
		return application.ErrPrecondition
	}
	if err := ensureNoPendingNativeScope(tx, project); err != nil {
		return err
	}
	var rows []serviceItemRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND project_id=?", tenant, projectID).Find(&rows).Error; err != nil {
		return err
	}
	return checkNativeScopeBaseline(tx, project, rows)
}

// guardNativeScopeMutation 在交付事件事务中阻断待审批原生项目的写入。
// 参数：tx 为事务，item 为已锁定的服务项。
// 返回值：合同保持原行为，未知来源及待审批失败关闭。
func guardNativeScopeMutation(tx *gorm.DB, item serviceItemRecord) error {
	var project projectRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", item.TenantID, item.ProjectID).Take(&project).Error
	// 缺失项目仍由外层ApplyDeliveryEvent的既有项目锁校验拒绝，不影响独立函数测试。
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if project.SourceKind == "" || project.SourceKind == "CONTRACT" {
		return nil
	}
	if project.SourceKind != "NATIVE" {
		return application.ErrPrecondition
	}
	return ensureNoPendingNativeScope(tx, project)
}

// saveNativeScopeEvent 同事务保存明确本地业务事件和审计outbox。
// 参数：ctx/tx 为请求事务，project/change 为真实坐标，action/actor 为实际动作与身份。
// 返回值：任一写入失败回滚全部范围状态。
func saveNativeScopeEvent(ctx context.Context, tx *gorm.DB, project projectRecord, change nativeScopeChangeRecord, action, actor string) error {
	now := time.Now().UTC()
	eventID := ulid.Make().String()
	payload, err := json.Marshal(map[string]any{"change_id": change.ID, "base_scope_version": change.BaseScopeVersion, "applied_scope_version": change.AppliedScopeVersion, "scope_hash": change.ScopeHash, "reason": change.Reason, "comment": change.Comment, "requested_by": change.RequestedBy, "decided_by": change.DecidedBy})
	if err != nil {
		return err
	}
	if err = tx.Create(&deliveryEventRecord{ID: eventID, TenantID: project.TenantID, ProjectID: project.ID, EventType: action, ActorUserID: actor, Payload: payload, CreatedAt: now}).Error; err != nil {
		return err
	}
	meta := auditctx.FromContext(ctx)
	return tx.Create(&auditOutboxRecord{ID: ulid.Make().String(), TenantID: project.TenantID, EventID: eventID, ActorID: actorPointer(actor), ActorType: "USER", Action: "PROJECT_MANAGEMENT:" + action, ResourceType: "native_scope_change", ResourceID: change.ID, RequestID: auditRequestID(meta.RequestID), LoginIP: auditIPBytes(meta.LoginIP), ClientIP: auditIPBytes(meta.ClientIP), IPSource: auditIPSource(meta.IPSource), Result: "SUCCESS", DeliveryStatus: "pending", NextAttemptAt: now, CreatedAt: now}).Error
}

// ValidateNativeScopeIfApplicable 仅对真实原生项目执行权威范围校验。
// 参数：ctx/filter 为当前授权，id 为待确认项目主键。
// 返回值：是否原生；未知来源、待审批和范围漂移返回错误。
func (r *Repository) ValidateNativeScopeIfApplicable(ctx context.Context, filter platform.ScopeFilter, id string) (bool, error) {
	native := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project projectRecord
		if err := applyProjectScope(tx.Model(&projectRecord{}), filter, "pm_project").Clauses(clause.Locking{Strength: "UPDATE"}).Where("pm_project.id=?", id).Take(&project).Error; err != nil {
			return mapNotFound(err)
		}
		if project.SourceKind == "" || project.SourceKind == "CONTRACT" {
			return nil
		}
		if project.SourceKind != "NATIVE" {
			return application.ErrPrecondition
		}
		native = true
		return guardNativeScopeConfirmation(tx, filter.TenantID, id)
	})
	return native, err
}

// nativeScopeCatalogFingerprint 冻结实际目录依据以识别审批期间的资格及配置漂移。
// 参数：tx 为项目锁内事务，tenant 为租户，items 为已验证范围。
// 返回值：含类别资格、方法配置及站点权威的摘要或读取错误。
func nativeScopeCatalogFingerprint(tx *gorm.DB, tenant string, items []domain.ServiceItem) (string, error) {
	facts := map[string]any{}
	for _, item := range items {
		var category detectionCategoryRecord
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND category=?", tenant, item.Category).Take(&category).Error; err != nil {
			return "", err
		}
		facts["category:"+item.Category] = category
		if item.SiteCode != "" {
			var site siteRecord
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND site_code=?", tenant, item.SiteCode).Take(&site).Error; err != nil {
				return "", err
			}
			facts["site:"+item.SiteCode] = site
		}
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

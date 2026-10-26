package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// isDuplicateKey 识别 MySQL 唯一键冲突（错误码 1062），把驱动的底层错误翻译成业务哨兵。
func isDeadlock(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// FindProjectByContract 按稳定合同身份查找项目。唯一性口径是「租户 + 合同身份」：
// contract_id 非空时以它为准，历史空 contract_id 行回退合同展示号（与迁移生成的
// contract_identity_key 完全一致）；普通合同版本只用于并发/审计，不参与查找或唯一性。
func (r *Repository) FindProjectByContract(ctx context.Context, filter platform.ScopeFilter, contractID, contractNumber string) (domain.Project, error) {
	identities := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, value := range []string{contractID, contractNumber} {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		identities = append(identities, value)
	}
	if len(identities) == 0 {
		return domain.Project{}, application.ErrNotFound
	}
	var record projectRecord
	err := projectContractIdentityQuery(r.db.WithContext(ctx), filter, identities).
		Order("created_at ASC, id ASC").Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Project{}, application.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	project := projectFromRecord(record)
	inputs, err := r.projectStatusInputs(ctx, filter.TenantID, []string{record.ID})
	if err != nil {
		return domain.Project{}, err
	}
	applyDerivedProjectMetrics(&project, inputs[record.ID])
	return project, nil
}

// projectContractIdentityQuery 是建项身份的唯一 SQL 口径：contract_id 非空取它，
// 历史空 ID 回退合同展示号；普通行版本不参与。与迁移生成的 contract_identity_key 一致。
func projectContractIdentityQuery(db *gorm.DB, filter platform.ScopeFilter, identities []string) *gorm.DB {
	return applyProjectScope(db.Model(&projectRecord{}), filter, "pm_project").
		Where("(CASE WHEN contract_id <> '' THEN contract_id ELSE contract END) IN ?", identities)
}

func (r *Repository) ActivateContract(ctx context.Context, project domain.Project, items []domain.ServiceItem, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, CustomerID: project.CustomerID, Contract: project.Contract, ContractID: project.ContractID, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := tx.Create(&pr).Error; err != nil {
			// 同一 (tenant_id, 合同身份) 并发激活时，唯一键
			// uq_pm_project_contract_identity 会让后到的事务失败；这里翻译成语义哨兵，
			// 由应用层回读已存在项目并按幂等成功返回。
			if isDuplicateKey(err) {
				return application.ErrDuplicateContract
			}
			return err
		}
		for _, item := range items {
			rec := serviceItemRecord{ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, RequiredCodes: jsonValue(item.RequiredCodes), Special: item.Special, TestMode: item.TestMode, Status: item.Status, TechReviewStatus: item.TechReviewStatus, ConflictStatus: item.ConflictStatus, StatusChangedAt: &project.CreatedAt, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		if err := createEvent(tx, event); err != nil {
			return err
		}
		return createNotificationOutbox(tx, event)
	})
}

func (r *Repository) CreateProjectWithServiceItems(ctx context.Context, project domain.Project, items []domain.ServiceItem) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, CustomerID: project.CustomerID, Contract: project.Contract, ContractID: project.ContractID, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := tx.Create(&pr).Error; err != nil {
			// 手动创建没有合同激活那样的幂等回读：撞唯一键 uq_pm_project_contract_identity
			// 说明同一合同已有项目，翻译成语义哨兵交由应用层给出可执行提示，
			// 而不是让 MySQL 1062 一路兜底成 500「服务暂不可用」。
			if isDuplicateKey(err) {
				return application.ErrDuplicateProject
			}
			return err
		}
		for _, item := range items {
			rec := serviceItemRecord{ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, RequiredCodes: jsonValue(item.RequiredCodes), Special: item.Special, TestMode: item.TestMode, Status: item.Status, TechReviewStatus: item.TechReviewStatus, ConflictStatus: item.ConflictStatus, StatusChangedAt: &project.CreatedAt, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) SyncContractStampStatus(ctx context.Context, project domain.Project, uploaded bool, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var latest deliveryEventRecord
		err := tx.Where("tenant_id = ? AND project_id = ? AND event_type = ?", project.TenantID, project.ID, application.EventContractStampStatus).Order("created_at DESC").Take(&latest).Error
		if err == nil {
			payload := map[string]any{}
			if json.Unmarshal(latest.Payload, &payload) == nil && payload["stamped_contract_uploaded"] == uploaded {
				return nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := createEvent(tx, event); err != nil {
			return err
		}
		return createNotificationOutbox(tx, event)
	})
}

func (r *Repository) ApplyDeliveryEvent(ctx context.Context, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if event.Type == application.EventDeviationReviewed {
			deviationID := stringValue(event.Payload, "deviation_id")
			var reported deliveryEventRecord
			if err := tx.Where("tenant_id=? AND event_type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deviation_id'))=?", event.TenantID, application.EventDeviationReported, deviationID).First(&reported).Error; err != nil {
				return mapNotFound(err)
			}
			event.ProjectID, event.ServiceItemID = reported.ProjectID, reported.ServiceItemID
		}
		if event.ServiceItemID != "" {
			var item serviceItemRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", event.TenantID, event.ServiceItemID).First(&item).Error; err != nil {
				return mapNotFound(err)
			}
			event.ProjectID = item.ProjectID
			if event.ClientOperationID != "" {
				var existing deliveryEventRecord
				err := tx.Where("tenant_id=? AND service_item_id=? AND actor_user_id=? AND client_operation_id=?", event.TenantID, event.ServiceItemID, event.ActorUserID, event.ClientOperationID).Take(&existing).Error
				if err == nil {
					payload, marshalErr := json.Marshal(event.Payload)
					if marshalErr != nil {
						return marshalErr
					}
					if existing.EventType == event.Type && jsonSemanticallyEqual(existing.Payload, payload) {
						return application.ErrAlreadyApplied
					}
					return application.ErrConflict
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}
			if err := applyItemEvent(tx, &item, event); err != nil {
				return err
			}
			if event.Type == application.EventExecutionTeamAssigned {
				if err := tx.Where("tenant_id=? AND id=?", item.TenantID, item.ID).Take(&item).Error; err != nil {
					return err
				}
				if err := ensureEmbeddedPenetrationPackage(tx, &item, event.ActorUserID, event.ID+"-penetration-package", event.CreatedAt); err != nil {
					return err
				}
			}
		}
		if event.ProjectID != "" {
			var project projectRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", event.TenantID, event.ProjectID).First(&project).Error; err != nil {
				return mapNotFound(err)
			}
			if err := applyProjectEvent(tx, &project, event); err != nil {
				return err
			}
		}
		// L4：事件落库后把派生项目状态回写 pm_project.status 缓存，与读侧派生口径保持一致。
		// 服务项状态在事务内推进（含直接落库的终止/偏离/现场完成），不再让存储列悄然陈旧。
		if event.ProjectID != "" {
			if err := syncProjectStatusColumn(tx, event.TenantID, event.ProjectID); err != nil {
				return err
			}
		}
		if err := createEvent(tx, event); err != nil {
			return err
		}
		return createNotificationOutbox(tx, event)
	})
}

func jsonValue(v any) []byte { b, _ := json.Marshal(v); return b }
func stringValue(values map[string]any, key string) string {
	v, _ := values[key].(string)
	return strings.TrimSpace(v)
}
func expectedVersionValue(values map[string]any) uint64 {
	switch value := values["expected_version"].(type) {
	case uint64:
		return value
	case uint:
		return uint64(value)
	case int:
		if value > 0 {
			return uint64(value)
		}
	case int64:
		if value > 0 {
			return uint64(value)
		}
	case float64:
		if value > 0 && value == float64(uint64(value)) {
			return uint64(value)
		}
	}
	return 0
}
func rfc3339Value(values map[string]any, key string) any {
	value := stringValue(values, key)
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return parsed
}
func timePtr(v time.Time) *time.Time {
	if v.IsZero() {
		return nil
	}
	return &v
}
func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return application.ErrNotFound
	}
	return err
}

// UpdateCapabilityIdentities 批量回写人员档案的身份复核结果。
// 只更新身份相关列，避免复核动作覆盖档案的业务字段。
func (auditOutboxRecord) TableName() string { return "prj_audit_outbox" }

func actorPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func auditIPBytes(value string) []byte {
	parsed := net.ParseIP(strings.TrimSpace(value))
	if parsed == nil {
		return nil
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		return append([]byte(nil), ipv4...)
	}
	return append([]byte(nil), parsed.To16()...)
}

func auditIPSource(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DIRECT", "TRUSTED_PROXY", "LEGACY_PROTOCOL", "UNKNOWN":
		return strings.ToUpper(strings.TrimSpace(value))
	default:
		return "UNKNOWN"
	}
}

// reworkReportRevision 处理“报告回退现场”的修订规则：
//  1. 当前修订置为 SUPERSEDED，保留原文件与审批证据，便于追溯；
//  2. 为下一个修订号创建 ACTIVE/NONE 的空修订，供重新编制使用（幂等）。
func reworkReportRevision(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	if err := tx.Model(&reportRevisionRecord{}).
		Where("tenant_id=? AND subject_type=? AND subject_id=? AND revision=? AND validity_status=?",
			item.TenantID, "SERVICE_ITEM", item.ID, item.ReportRevision, "ACTIVE").
		Updates(map[string]any{"validity_status": "SUPERSEDED", "updated_at": event.CreatedAt}).Error; err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&reportRevisionRecord{}).
		Where("tenant_id=? AND subject_type=? AND subject_id=? AND revision=?",
			item.TenantID, "SERVICE_ITEM", item.ID, item.ReportRevision+1).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return tx.Create(&reportRevisionRecord{
		TenantID: item.TenantID, ServiceItemID: item.ID, SubjectType: "SERVICE_ITEM", SubjectID: item.ID,
		Revision: item.ReportRevision + 1, Status: "NONE", ValidityStatus: "ACTIVE",
		CreatedAt: event.CreatedAt, UpdatedAt: event.CreatedAt,
	}).Error
}

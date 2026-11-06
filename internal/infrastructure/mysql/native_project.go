package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/auditctx"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type nativeProjectCommandRecord struct {
	TenantID    string `gorm:"primaryKey;size:64"`
	SourceID    string `gorm:"primaryKey;size:64"`
	Fingerprint string `gorm:"size:64;not null"`
	ProjectID   string `gorm:"size:32;not null"`
}

// TableName 返回原生委托幂等围栏表名。
// 参数：无。
// 返回值：固定表名。
func (nativeProjectCommandRecord) TableName() string { return "pm_native_project_command" }

type nativeCounterpartyRecord struct {
	TenantID  string `gorm:"primaryKey;size:64"`
	ID        string `gorm:"primaryKey;size:64"`
	Name      string `gorm:"size:255;not null"`
	CreatedAt time.Time
}

// TableName 返回本地交易对方表名。
// 参数：无。
// 返回值：固定表名。
func (nativeCounterpartyRecord) TableName() string { return "pm_native_counterparty" }

type nativeScopeRecord struct {
	TenantID        string          `gorm:"primaryKey;size:64"`
	ProjectID       string          `gorm:"primaryKey;size:32"`
	Version         uint64          `gorm:"primaryKey"`
	SourceID        string          `gorm:"size:64;not null"`
	ActorIdentityID string          `gorm:"size:128;not null"`
	ServiceItems    json.RawMessage `gorm:"type:json;not null"`
	CreatedAt       time.Time
}

// TableName 返回不可变原生范围版本表名。
// 参数：无。
// 返回值：固定表名。
func (nativeScopeRecord) TableName() string { return "pm_native_scope_version" }

// CreateNativeProject 原子保存本地委托、交易对方、项目和首次范围版本。
// 参数：ctx 为请求，filter 为真实授权范围，project/items 为服务端范围，fingerprint 为幂等摘要。
// 返回值：新项目或同载荷的已有项目；跨范围、改载荷与事务失败返回错误。
func (r *Repository) CreateNativeProject(ctx context.Context, filter platform.ScopeFilter, project domain.Project, items []domain.ServiceItem, fingerprint string) (domain.Project, error) {
	if project.SourceKind != "NATIVE" || project.ContractID != "" || project.Contract != "" || project.ContractVersion != "" || project.ScopeVersion != 1 || project.SourceID == "" || project.TenantID != filter.TenantID || len(items) == 0 || len(items) > 200 || fingerprint == "" {
		return domain.Project{}, application.ErrValidation
	}
	var result domain.Project
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		command := nativeProjectCommandRecord{TenantID: project.TenantID, SourceID: project.SourceID, Fingerprint: fingerprint}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&command).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND source_id=?", project.TenantID, project.SourceID).Take(&command).Error; err != nil {
			return err
		}
		if command.Fingerprint != fingerprint {
			return application.ReasonError{Kind: application.ErrConflict, Reason: "委托编号已用于不同的项目范围，请核对后重试"}
		}
		if command.ProjectID != "" {
			var row projectRecord
			err := applyProjectScope(tx.Model(&projectRecord{}), filter, "pm_project").Where("id=?", command.ProjectID).Take(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return application.ErrForbidden
			}
			result = projectFromRecord(row)
			return err
		}
		if err := saveNativeCounterparty(tx, project); err != nil {
			return err
		}
		if err := saveNativeProjectRows(tx, project, items); err != nil {
			return err
		}
		if err := saveNativeProjectAudit(ctx, tx, project); err != nil {
			return err
		}
		if err := tx.Model(&nativeProjectCommandRecord{}).Where("tenant_id=? AND source_id=?", project.TenantID, project.SourceID).Update("project_id", project.ID).Error; err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// saveNativeCounterparty 在当前租户内保留稳定交易对方身份且禁止同编号静默改名。
// 参数：tx 为业务事务，project 包含本地交易对方快照。
// 返回值：存储错误或同编号名称冲突。
func saveNativeCounterparty(tx *gorm.DB, project domain.Project) error {
	row := nativeCounterpartyRecord{TenantID: project.TenantID, ID: project.CustomerID, Name: project.Customer, CreatedAt: project.CreatedAt}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", project.TenantID, project.CustomerID).Take(&row).Error; err != nil {
		return err
	}
	if row.Name != project.Customer {
		return application.ReasonError{Kind: application.ErrConflict, Reason: "本地委托单位编号已对应其他名称，请核对编号"}
	}
	return nil
}

// saveNativeProjectRows 保存项目、待确认服务项及不可变的初始范围证据。
// 参数：tx 为业务事务，project 为原生项目，items 为受控本地范围。
// 返回值：任一写入失败返回错误，使整个建项事务回滚。
func saveNativeProjectRows(tx *gorm.DB, project domain.Project, items []domain.ServiceItem) error {
	row := projectRecord{ID: project.ID, TenantID: project.TenantID, SourceKind: "NATIVE", SourceID: project.SourceID, ScopeVersion: 1,
		OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, CustomerID: project.CustomerID,
		SupplementStatus: "NONE", Services: len(items), Team: project.Team, Manager: project.Manager,
		OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID,
		Status: project.Status, Due: project.Due, Version: 1, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	for _, item := range items {
		record := serviceItemRecord{ID: item.ID, TenantID: project.TenantID, ProjectID: project.ID, SourceServiceID: item.SourceServiceID,
			Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement,
			System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, Special: item.Special,
			TestMode: item.TestMode, RequiredCodes: jsonValue(item.RequiredCodes), Status: item.Status, ConflictStatus: item.ConflictStatus,
			Version: 1, StatusChangedAt: &project.CreatedAt, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
	}
	payload, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return tx.Create(&nativeScopeRecord{TenantID: project.TenantID, ProjectID: project.ID, Version: 1, SourceID: project.SourceID,
		ActorIdentityID: project.OwnerIdentityID, ServiceItems: payload, CreatedAt: project.CreatedAt}).Error
}

// saveNativeProjectAudit 将建项审计意图和项目范围一起提交，重放不重复投递。
// 参数：ctx 为请求审计上下文，tx 为业务事务，project 为本地项目。
// 返回值：审计落库失败时返回错误并回滚整个建项。
func saveNativeProjectAudit(ctx context.Context, tx *gorm.DB, project domain.Project) error {
	meta := auditctx.FromContext(ctx)
	actor := firstValue(meta.ActorID, project.OwnerIdentityID)
	return tx.Create(&auditOutboxRecord{ID: ulid.Make().String(), TenantID: project.TenantID, EventID: ulid.Make().String(),
		ActorID: actorPointer(actor), ActorType: "USER", Action: "PROJECT_MANAGEMENT:NATIVE_PROJECT_CREATED", ResourceType: "project", ResourceID: project.ID,
		RequestID: auditRequestID(meta.RequestID), LoginIP: auditIPBytes(meta.LoginIP), ClientIP: auditIPBytes(meta.ClientIP), IPSource: auditIPSource(meta.IPSource),
		Result: "SUCCESS", DeliveryStatus: "pending", NextAttemptAt: project.CreatedAt, CreatedAt: project.CreatedAt}).Error
}

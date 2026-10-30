package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type contractImportBindingRecord struct {
	TenantID           string `gorm:"primaryKey;size:64;uniqueIndex:uq_pm_import_item,priority:1;uniqueIndex:uq_pm_import_contract,priority:1"`
	CommandID          string `gorm:"primaryKey;size:32"`
	OriginImportItemID string `gorm:"size:64;not null;uniqueIndex:uq_pm_import_item,priority:2"`
	SourceApplication  string `gorm:"size:64;not null;uniqueIndex:uq_pm_import_contract,priority:2"`
	ContractID         string `gorm:"size:64;not null;uniqueIndex:uq_pm_import_contract,priority:3"`
	ProjectID          string `gorm:"size:32;not null"`
	Relationship       string `gorm:"size:16;not null"`
	SourceRevision     uint64 `gorm:"not null"`
	PolicyDigest       string `gorm:"size:64;not null"`
	ReleaseVersion     uint64 `gorm:"not null"`
	PayloadSHA256      string `gorm:"size:64;not null"`
	Receipt            []byte `gorm:"type:json"`
	BoundBy            string `gorm:"size:64;not null"`
	CreatedAt          time.Time
}

// TableName 指定导入命令及关系映射共用的持久表。
// 参数：无。
// 返回值：表名。
func (contractImportBindingRecord) TableName() string { return "pm_contract_import_binding" }

// GetContractImportCommand 读取同租户已完成命令和冻结载荷摘要。
// 参数：ctx 为请求，tenant 为可信租户，command 为稳定命令编号。
// 返回值：原回执、载荷摘要和存储错误。
func (r *Repository) GetContractImportCommand(ctx context.Context, tenant, command string) (domain.ContractImportBindingReceipt, string, error) {
	var row contractImportBindingRecord
	err := r.db.WithContext(ctx).Where("tenant_id=? AND command_id=?", tenant, command).Take(&row).Error
	if err != nil {
		return domain.ContractImportBindingReceipt{}, "", mapNotFound(err)
	}
	var receipt domain.ContractImportBindingReceipt
	if err := json.Unmarshal(row.Receipt, &receipt); err != nil {
		return receipt, "", err
	}
	return receipt, row.PayloadSHA256, nil
}

// GetContractImportBinding 读取来源项实际持久的完成回执。
// 参数：ctx 为请求，tenant 为可信租户，item 为来源项。
// 返回值：原回执或不存在、存储错误。
func (r *Repository) GetContractImportBinding(ctx context.Context, tenant, item string) (domain.ContractImportBindingReceipt, error) {
	var row contractImportBindingRecord
	err := r.db.WithContext(ctx).Where("tenant_id=? AND origin_import_item_id=?", tenant, item).Take(&row).Error
	if err != nil {
		return domain.ContractImportBindingReceipt{}, mapNotFound(err)
	}
	var receipt domain.ContractImportBindingReceipt
	err = json.Unmarshal(row.Receipt, &receipt)
	return receipt, err
}

// BindContractImport 将命令身份、项目变更及最终回执原子提交。
// 参数：ctx 为请求，input 为冻结命令，digest 为载荷摘要，enabled 控制新增，create 复用拆解。
// 返回值：稳定完成回执；重放异内容、身份或版本冲突时拒绝。
func (r *Repository) BindContractImport(
	ctx context.Context, input domain.ContractImportBindingCommand, digest string, enabled bool,
	create func(application.Repository) (domain.Project, error),
) (domain.ContractImportBindingReceipt, error) {
	var receipt domain.ContractImportBindingReceipt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockContractIdentities(tx, input.TenantID, input.ContractID, input.Contract.ContractNumber); err != nil {
			return err
		}
		row := contractImportBindingRecord{
			TenantID: input.TenantID, CommandID: input.CommandID, OriginImportItemID: input.OriginImportItemID,
			SourceApplication: "contract_management", ContractID: input.ContractID,
			Relationship: input.Relationship, SourceRevision: input.SourceRevision,
			PolicyDigest: input.PolicyDigest, ReleaseVersion: input.ReleaseVersion, PayloadSHA256: digest,
			BoundBy: "contract_management", CreatedAt: time.Now().UTC(),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		var locked contractImportBindingRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND command_id=?", input.TenantID, input.CommandID).Take(&locked).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return application.ErrImportConflict
		}
		if err != nil {
			return err
		}
		if locked.PayloadSHA256 != digest {
			return application.ErrImportConflict
		}
		if len(locked.Receipt) > 0 {
			return json.Unmarshal(locked.Receipt, &receipt)
		}
		if !enabled {
			return application.ErrImportDisabled
		}
		target := ""
		if input.ProjectMode == "LINK_EXISTING" {
			target = input.ExistingProjectID
		}
		if err = claimContractProject(tx, input.TenantID, input.ContractID, input.Contract.ContractNumber, target, input.CommandID); err != nil {
			return err
		}
		project, err := bindImportProject(tx, input, create)
		if err != nil {
			return err
		}
		if err = claimContractProject(tx, input.TenantID, input.ContractID, input.Contract.ContractNumber, project.ID, input.CommandID); err != nil {
			return err
		}
		receipt = domain.ContractImportBindingReceipt{
			CommandID: input.CommandID, SchemaVersion: 1, TenantID: input.TenantID,
			OriginImportItemID: input.OriginImportItemID, SourceRevision: input.SourceRevision,
			PolicyDigest: input.PolicyDigest, ReleaseVersion: input.ReleaseVersion,
			ContractID: input.ContractID, ProjectID: project.ID, ProjectVersion: project.Version,
			Status: "PROJECT_BOUND",
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		return tx.Model(&contractImportBindingRecord{}).
			Where("tenant_id=? AND command_id=?", input.TenantID, input.CommandID).
			Updates(map[string]any{"project_id": project.ID, "receipt": raw}).Error
	})
	return receipt, err
}

// bindImportProject 在接续事务内创建项目或校验并绑定既有项目。
// 参数：tx 为命令事务，input 为冻结命令，create 为合法拆解回调。
// 返回值：项目及其绑定后版本，失败时事务整体回滚。
func bindImportProject(tx *gorm.DB, input domain.ContractImportBindingCommand, create func(application.Repository) (domain.Project, error)) (domain.Project, error) {
	if input.ProjectMode == "CREATE_NEW" {
		project, err := create(&Repository{db: tx, importCommandID: input.CommandID})
		if err != nil {
			return project, err
		}
		var row projectRecord
		err = tx.Where("tenant_id=? AND id=?", input.TenantID, project.ID).Take(&row).Error
		return projectFromRecord(row), err
	}
	var row projectRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", input.TenantID, input.ExistingProjectID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Project{}, application.ErrImportConflict
	}
	if err != nil {
		return domain.Project{}, err
	}
	if row.Version != input.ExpectedProjectVersion || row.CustomerID == "" ||
		row.CustomerID != input.Contract.CustomerID || row.Status == domain.ProjectStatusTerminated {
		return domain.Project{}, application.ErrImportConflict
	}
	if input.OriginalContractRef != row.ContractID && input.OriginalContractRef != row.Contract {
		return domain.Project{}, application.ErrImportConflict
	}
	if input.Relationship == "PRIMARY" && input.ContractID != row.ContractID {
		return domain.Project{}, application.ErrImportConflict
	}
	if input.Relationship == "SUPPLEMENT" && (input.ContractID == row.ContractID || input.Contract.ContractNumber == row.Contract) {
		return domain.Project{}, application.ErrImportConflict
	}
	err = tx.Model(&projectRecord{}).
		Where("tenant_id=? AND id=? AND version=?", input.TenantID, row.ID, row.Version).
		Updates(map[string]any{"version": row.Version + 1, "updated_at": time.Now().UTC()}).Error
	if err != nil {
		return domain.Project{}, err
	}
	row.Version++
	return projectFromRecord(row), nil
}

package mysql

import (
	"errors"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
	"strings"
)

type contractIdentityGuardRecord struct {
	TenantID         string `gorm:"primaryKey;size:64"`
	ContractIdentity string `gorm:"primaryKey;size:128"`
	ProjectID        string `gorm:"size:32;not null;default:''"`
}

// TableName 指定合同跨关系唯一归属的持久围栏表。
// 参数：无。
// 返回值：固定表名。
func (contractIdentityGuardRecord) TableName() string { return "pm_contract_identity_guard" }

// lockContractIdentities 按稳定顺序锁来源ID及兼容展示号，存在与不存在的身份都串行。
// 参数：tx为业务事务，tenant为可信租户，id与number为合同身份。
// 返回值：锁定身份列表或存储错误；不依赖数据库默认隔离级别的间隙锁。
func lockContractIdentities(tx *gorm.DB, tenant, id, number string) ([]string, error) {
	identities := []string{}
	seen := map[string]bool{}
	for _, value := range []string{id, number} {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			identities = append(identities, value)
			seen[value] = true
		}
	}
	sort.Strings(identities)
	for _, identity := range identities {
		row := contractIdentityGuardRecord{TenantID: tenant, ContractIdentity: identity}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return nil, err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND contract_identity=?", tenant, identity).Take(&row).Error; err != nil {
			return nil, err
		}
	}
	return identities, nil
}

// claimContractProject 在统一身份锁下校验主合同、导入映射和已声明归属。
// 参数：tx为业务事务，tenant/id/number限定合同，project为目标项目或新建前空值，command为本事务导入命令。
// 返回值：异项目冲突或存储错误；合法归属与业务写入同事务落库。
func claimContractProject(tx *gorm.DB, tenant, id, number, project, command string) error {
	identities, err := lockContractIdentities(tx, tenant, id, number)
	if err != nil || len(identities) == 0 {
		return err
	}
	var primary []projectRecord
	if err = projectContractIdentityQuery(tx, platform.ScopeFilter{TenantID: tenant, AllowAll: true}, identities).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&primary).Error; err != nil {
		return err
	}
	for _, row := range primary {
		if project == "" || row.ID != project {
			return application.ErrImportConflict
		}
	}
	var bindings []contractImportBindingRecord
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND source_application=? AND contract_id IN ?", tenant, "contract_management", identities).Find(&bindings).Error; err != nil {
		return err
	}
	for _, row := range bindings {
		if row.ProjectID == "" && row.CommandID == command && command != "" {
			continue
		}
		if project == "" || row.ProjectID != project {
			return application.ErrImportConflict
		}
	}
	for _, identity := range identities {
		var guard contractIdentityGuardRecord
		err = tx.Where("tenant_id=? AND contract_identity=?", tenant, identity).Take(&guard).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return application.ErrImportConflict
		}
		if err != nil {
			return err
		}
		if guard.ProjectID != "" && (project == "" || guard.ProjectID != project) {
			return application.ErrImportConflict
		}
		if project != "" {
			if err = tx.Model(&guard).Update("project_id", project).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// checkOrdinarySupplementImport 保留普通合同跨项目补充规则，仅拒绝专用历史绑定冲突。
// 参数：tx为事件事务，tenant/id限定普通补充合同，project为本次项目。
// 返回值：专用导入异项目冲突或数据库错误；不认领普通补充的独占归属。
func checkOrdinarySupplementImport(tx *gorm.DB, tenant, id, project string) error {
	if _, err := lockContractIdentities(tx, tenant, id, ""); err != nil {
		return err
	}
	var count int64
	err := tx.Model(&contractImportBindingRecord{}).Where("tenant_id=? AND source_application=? AND contract_id=? AND project_id<>'' AND project_id<>?", tenant, "contract_management", id, project).Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return application.ErrImportConflict
	}
	return nil
}

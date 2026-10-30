package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

var (
	ErrImportDisabled = errors.New("new project contract imports are disabled")
	ErrImportConflict = errors.New("project contract import identity conflict")
	ErrImportPolicy   = errors.New("project contract import policy unsupported")
)

// ContractImportRepository 保证机器命令、业务项目及来源映射同事务持久化。
type ContractImportRepository interface {
	GetContractImportCommand(context.Context, string, string) (domain.ContractImportBindingReceipt, string, error)
	GetContractImportBinding(context.Context, string, string) (domain.ContractImportBindingReceipt, error)
	BindContractImport(
		context.Context, domain.ContractImportBindingCommand, string, bool,
		func(Repository) (domain.Project, error),
	) (domain.ContractImportBindingReceipt, error)
}

// BindImportedContract 校验可信租户和负责人后执行原子接续并支持精确重放。
// 参数：ctx 为请求，p 为机器主体，input 为冻结命令，enabled 控制新写入。
// 返回值：持久完成回执；政策、来源或版本不一致时返回错误。
func (s *Service) BindImportedContract(
	ctx context.Context, p platform.Principal,
	input domain.ContractImportBindingCommand, enabled bool,
) (domain.ContractImportBindingReceipt, error) {
	empty := domain.ContractImportBindingReceipt{}
	if !p.Has("project.contract.import") || p.TenantID == "" || input.TenantID != p.TenantID {
		return empty, ErrForbidden
	}
	if err := validateImportBinding(input); err != nil {
		return empty, err
	}
	repo, ok := s.Repo.(ContractImportRepository)
	if !ok {
		return empty, ErrContractUnavailable
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return empty, ErrValidation
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	receipt, prior, err := repo.GetContractImportCommand(ctx, p.TenantID, input.CommandID)
	if err == nil {
		if prior != digest {
			return empty, ErrImportConflict
		}
		return receipt, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return empty, err
	}
	if !enabled {
		return empty, ErrImportDisabled
	}
	owner, err := s.resolveCapabilityPerson(ctx, input.TargetOwnerUserID)
	if err != nil {
		return empty, err
	}
	if err := s.ensurePersonnelNotFrozen(ctx, p.TenantID, owner.UserID); err != nil {
		return empty, err
	}
	return repo.BindContractImport(ctx, input, digest, enabled, func(tx Repository) (domain.Project, error) {
		scoped := *s
		scoped.Repo = tx
		contract := input.Contract
		contract.OriginKind = "IMPORTED"
		contract.ImportMode = input.ImportMode
		contract.OriginImportItemID = input.OriginImportItemID
		contract.StampedContractUploaded = false
		return scoped.activateContract(ctx, p, contract, true, &owner)
	})
}

// validateImportBinding 拒绝非法版本、历史政策和含糊的目标关系。
// 参数：input 为接收的版本化机器命令。
// 返回值：参数或政策错误，合法时为空。
func validateImportBinding(input domain.ContractImportBindingCommand) error {
	supportedProject := input.ProjectMode == "CREATE_NEW" || input.ProjectMode == "LINK_EXISTING"
	if input.ImportMode != "CONTINUE_LIVE" || !supportedProject {
		return ErrImportPolicy
	}
	validIdentity := input.SchemaVersion == 1 && validImportHex(input.CommandID, 32) &&
		validImportHex(input.PolicyDigest, 64) && input.SourceRevision > 0 && input.ReleaseVersion > 0
	if !validIdentity {
		return ErrValidation
	}
	for _, value := range []string{
		input.TenantID, input.ContractID, input.ContractVersion,
		input.OriginImportItemID, input.TargetOwnerUserID,
	} {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 64 {
			return ErrValidation
		}
	}
	if input.Relationship != "PRIMARY" && input.Relationship != "SUPPLEMENT" {
		return ErrValidation
	}
	contract := input.Contract
	matchingContract := contract.ContractID == input.ContractID &&
		contract.ContractVersion == input.ContractVersion
	completeContract := strings.TrimSpace(contract.ContractNumber) != "" &&
		strings.TrimSpace(contract.CustomerID) != "" && strings.TrimSpace(contract.Customer) != "" &&
		!contract.EffectiveAt.IsZero() && len(contract.Services) > 0 && len(contract.Services) <= 20
	if !matchingContract || !completeContract {
		return ErrValidation
	}
	if contract.OriginKind != "" && contract.OriginKind != "IMPORTED" {
		return ErrValidation
	}
	if contract.ImportMode != "" && contract.ImportMode != input.ImportMode {
		return ErrValidation
	}
	if contract.OriginImportItemID != "" && contract.OriginImportItemID != input.OriginImportItemID {
		return ErrValidation
	}
	if err := validateImportContractServices(contract); err != nil {
		return err
	}
	if input.ProjectMode == "LINK_EXISTING" {
		validLink := input.ExistingProjectID != "" && len(input.ExistingProjectID) <= 32 &&
			input.ExpectedProjectVersion > 0 && input.OriginalContractRef != "" &&
			len(input.OriginalContractRef) <= 64
		if !validLink {
			return ErrValidation
		}
	} else if input.Relationship != "PRIMARY" || input.ExistingProjectID != "" ||
		input.ExpectedProjectVersion != 0 || input.OriginalContractRef != "" {
		return ErrValidation
	}
	return nil
}

// validateImportContractServices 校验合同投影的存储边界和服务身份唯一性。
// 参数：contract 为接续合同的最小服务投影。
// 返回值：非法日期、过长字段、重复服务或不支持的方法返回参数错误。
func validateImportContractServices(contract domain.ContractActivation) error {
	if contract.EffectiveAt.Year() < 1000 || contract.EffectiveAt.Year() > 9999 {
		return ErrValidation
	}
	for _, field := range []struct {
		value string
		max   int
		label string
	}{
		{contract.ContractNumber, 64, "合同编号"}, {contract.CustomerID, 64, "客户标识"},
		{contract.Customer, 255, "客户名称"}, {contract.ContractName, 255, "合同名称"},
	} {
		if err := validateProjectTextLength(field.value, field.max, field.label); err != nil {
			return err
		}
	}
	if err := validateContractServiceTexts(contract.Services); err != nil {
		return err
	}
	seen := make(map[string]bool, len(contract.Services))
	for _, service := range contract.Services {
		if strings.TrimSpace(service.SourceID) == "" || strings.TrimSpace(service.Site) == "" || strings.TrimSpace(service.Batch) == "" || strings.TrimSpace(service.Category) == "" || seen[service.SourceID] {
			return ErrValidation
		}
		if service.TestMode != "STANDARD" && service.TestMode != "PENETRATION" {
			return ErrValidation
		}
		seen[service.SourceID] = true
	}
	return nil
}

// validImportHex 检查协议中的小写定长十六进制标识。
// 参数：value 为标识，size 为要求长度。
// 返回值：格式满足时为真。
func validImportHex(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// GetImportedContractBinding 查询同可信租户的真实持久项目映射。
// 参数：ctx 为请求，p 为机器主体，itemID 为来源项标识。
// 返回值：原绑定回执或权限、不存在、存储错误。
func (s *Service) GetImportedContractBinding(
	ctx context.Context, p platform.Principal, itemID string,
) (domain.ContractImportBindingReceipt, error) {
	if !p.Has("project.contract.import") || p.TenantID == "" {
		return domain.ContractImportBindingReceipt{}, ErrForbidden
	}
	repo, ok := s.Repo.(ContractImportRepository)
	if !ok {
		return domain.ContractImportBindingReceipt{}, ErrContractUnavailable
	}
	return repo.GetContractImportBinding(ctx, p.TenantID, itemID)
}

package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// NativeProjectRepository 将本地委托、项目及首次服务范围放入同一事务。
type NativeProjectRepository interface {
	CreateNativeProject(context.Context, platform.ScopeFilter, domain.Project, []domain.ServiceItem, string) (domain.Project, error)
}

var nativeReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

// CreateNativeProject 在显式启用模式下建立本地委托项目和待确认服务范围。
// 参数：ctx 为请求，p 为真实用户主体，input 为本地项目，requested 为服务范围。
// 返回值：新建或相同请求重放的项目；越权、无效范围及冲突时返回错误。
func (s *Service) CreateNativeProject(ctx context.Context, p platform.Principal, input domain.Project, requested []domain.ContractService) (domain.Project, error) {
	if !mayCreateProject(p) {
		return domain.Project{}, ErrForbidden
	}
	filter, err := authorizeProjectScope(p, "project.create")
	if err != nil {
		return domain.Project{}, err
	}
	if !s.NativeEnabled {
		return domain.Project{}, PreconditionError("原生委托建项未启用")
	}
	input, err = normalizeNativeProject(p, filter, input)
	if err != nil {
		return domain.Project{}, err
	}
	if err = s.ensureManagerNotFrozen(ctx, p.TenantID, input.ManagerIdentityID); err != nil {
		return domain.Project{}, err
	}
	items, err := s.nativeServiceItems(ctx, p.TenantID, requested)
	if err != nil {
		return domain.Project{}, err
	}
	fingerprint, err := nativeProjectFingerprint(input, items)
	if err != nil {
		return domain.Project{}, err
	}
	now := time.Now().UTC()
	input.ID, input.CreatedAt, input.UpdatedAt = projectID(now), now, now
	input.Services = len(items)
	for index := range items {
		items[index].ProjectID = input.ID
		items[index].ID = fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(input.ID, "PJ-"), index+1)
	}
	repo, ok := s.Repo.(NativeProjectRepository)
	if !ok {
		return domain.Project{}, fmt.Errorf("native project repository unavailable")
	}
	return repo.CreateNativeProject(ctx, filter, input, items, fingerprint)
}

// normalizeNativeProject 校验本地引用并仅保留允许创建的业务字段。
// 参数：p 为真实主体，filter 为创建范围，input 为待校验输入。
// 返回值：服务端归一化的原生项目；来源混用、越权或非法文本返回错误。
func normalizeNativeProject(p platform.Principal, filter platform.ScopeFilter, input domain.Project) (domain.Project, error) {
	if input.SourceKind != "NATIVE" || input.ContractID != "" || input.Contract != "" || input.ContractVersion != "" {
		return domain.Project{}, ValidationError("原生项目不能指定合同来源或合同引用")
	}
	input.Name, input.Customer = strings.TrimSpace(input.Name), strings.TrimSpace(input.Customer)
	input.CustomerID, input.SourceID = strings.TrimSpace(input.CustomerID), strings.TrimSpace(input.SourceID)
	if input.Name == "" || input.Customer == "" || !nativeReferencePattern.MatchString(input.CustomerID) || !nativeReferencePattern.MatchString(input.SourceID) {
		return domain.Project{}, ValidationError("请填写项目、委托单位和有效的本地委托编号")
	}
	for _, field := range []struct {
		value string
		max   int
		label string
	}{
		{input.Name, 255, "项目名称"}, {input.Customer, 255, "委托单位"}, {input.Team, 128, "团队"},
		{input.Manager, 128, "项目经理"}, {input.Due, 64, "项目期限"}, {input.OwnerOrgID, 64, "所属组织"},
		{input.ManagerIdentityID, 128, "项目经理标识"},
	} {
		if err := validateProjectTextLength(field.value, field.max, field.label); err != nil {
			return domain.Project{}, err
		}
	}
	if !filter.AllowAll {
		if input.OwnerOrgID != "" && !contains(filter.OrganizationIDs, input.OwnerOrgID) {
			return domain.Project{}, ErrForbidden
		}
		if input.OwnerOrgID == "" && len(filter.OrganizationIDs) == 1 {
			input.OwnerOrgID = filter.OrganizationIDs[0]
		}
		if !filter.AllowSelf && input.OwnerOrgID == "" {
			return domain.Project{}, ErrForbidden
		}
	}
	return domain.Project{TenantID: p.TenantID, SourceKind: "NATIVE", SourceID: input.SourceID, ScopeVersion: 1,
		Name: input.Name, Customer: input.Customer, CustomerID: input.CustomerID,
		Team: firstNonEmpty(strings.TrimSpace(input.Team), "未分配"), Manager: firstNonEmpty(strings.TrimSpace(input.Manager), "—"),
		OwnerOrgID: input.OwnerOrgID, OwnerIdentityID: firstNonEmpty(p.IdentityID, p.UserID),
		ManagerIdentityID: input.ManagerIdentityID, Due: strings.TrimSpace(input.Due),
		Status: domain.ProjectStatusPendingDecomposition, SupplementStatus: "NONE", Version: 1}, nil
}

// nativeServiceItems 按本地检测目录生成范围快照并保留现有确认和特殊方法门禁。
// 参数：ctx 为请求，tenant 为可信租户，requested 为人工录入范围。
// 返回值：待确认服务项；未知类别、无效方法或站点时返回错误。
func (s *Service) nativeServiceItems(ctx context.Context, tenant string, requested []domain.ContractService) ([]domain.ServiceItem, error) {
	if len(requested) == 0 || len(requested) > 200 {
		return nil, ValidationError("每个项目需包含1至200个服务项")
	}
	if err := validateContractServiceTexts(requested); err != nil {
		return nil, err
	}
	reader, ok := s.Repo.(detectionCategoryReader)
	if !ok {
		return nil, PreconditionError("本地检测类别目录不可用")
	}
	catalog, err := reader.ListDetectionCategories(ctx, tenant)
	if err != nil {
		return nil, err
	}
	entries := map[string]domain.DetectionCategory{}
	for _, entry := range catalog {
		if entry.Enabled {
			entries[splitCategoryKey(entry.Category)] = entry
		}
	}
	result := make([]domain.ServiceItem, 0, len(requested))
	for index, source := range requested {
		entry, exists := entries[splitCategoryKey(source.Category)]
		mode := strings.ToUpper(firstNonEmpty(strings.TrimSpace(source.TestMode), "STANDARD"))
		if !exists || strings.TrimSpace(source.Site) == "" || (mode != "STANDARD" && mode != "PENETRATION") {
			return nil, ValidationError("请填写实施场所并选择启用的检测类别和有效方法")
		}
		if entry.SpecialMethod == domain.SpecialMethodRequired {
			mode = "PENETRATION"
		}
		site, code, err := s.resolveActiveSite(ctx, tenant, source.Site, source.SiteCode)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.ServiceItem{TenantID: tenant, SourceServiceID: fmt.Sprintf("NATIVE-%03d", index+1),
			Batch: firstNonEmpty(strings.TrimSpace(source.Batch), "第一批"), Site: strings.TrimSpace(site), SiteCode: code,
			Category: entry.Category, Requirement: strings.TrimSpace(source.Requirement), System: strings.TrimSpace(source.System),
			SystemLevel: strings.TrimSpace(source.SystemLevel), SystemStandard: entry.SystemStandard,
			RequiredCodes: normalizeCapabilityCodes(domain.SplitCapabilityCodes(entry.RequiredCodes)),
			TestMode:      mode, Special: yesNo(mode == "PENETRATION"), Status: domain.ServiceItemStatusPendingConfirm, ConflictStatus: "UNCHECKED"})
	}
	return result, nil
}

// nativeProjectFingerprint 为归一化委托和初始范围计算稳定幂等摘要。
// 参数：project 为生成编号前的项目，items 为未分配持久编号的范围。
// 返回值：SHA256十六进制摘要；序列化失败时返回错误。
func nativeProjectFingerprint(project domain.Project, items []domain.ServiceItem) (string, error) {
	data, err := json.Marshal(struct {
		Project domain.Project
		Items   []domain.ServiceItem
	}{project, items})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

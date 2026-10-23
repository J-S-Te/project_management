package platform

import (
	"errors"
	"sort"
	"strings"
)

var ErrDataScopeDenied = errors.New("project data scope denied")

type DataScope struct {
	RoleCode        string `json:"role_code"`
	ScopeType       string `json:"scope_type"`
	ScopeID         string `json:"scope_id"`
	EnvironmentCode string `json:"environment_code"`
}

type Principal struct {
	Subject               string
	TenantID              string
	IdentityID            string
	PersonID              string
	UserID                string
	DisplayName           string
	Roles                 []string
	Permissions           map[string]bool
	DataScopes            []DataScope
	AuthorizationRevision uint64
	CatalogVersion        string
}

func (p Principal) Has(permission string) bool { return p.Permissions[permission] }

// HasFullDataScope 用于无法安全按单个项目、组织
// 或人员筛选的租户全范围主数据。非空的
// 窄范围不代表应用授权。
func (p Principal) HasFullDataScope() bool {
	for _, scope := range p.DataScopes {
		switch scope.ScopeType {
		case "APPLICATION", "ENVIRONMENT", "TENANT":
			return true
		}
	}
	return false
}

// HasOrganizationalScope 判断主体是否持有组织级
// 授权（应用、环境、租户或组织）。能力或设备等目录主
// 数据允许任一组织级角色读取；
// 项目或个人范围仍排除在外，防止向窄范围
// 暴露租户全范围主数据。
func (p Principal) HasOrganizationalScope() bool {
	for _, scope := range p.DataScopes {
		switch scope.ScopeType {
		case "APPLICATION", "ENVIRONMENT", "TENANT", "ORG":
			return true
		}
	}
	return false
}

// ScopeFilter 是仓储接受的唯一业务数据边界。
// 平台当前以 APPLICATION 暴露租户全应用授权，
// 以 ENVIRONMENT 暴露环境全范围授权，因此在授权上下文
// 绑定检查后，两者均为显式全数据范围。
type ScopeFilter struct {
	TenantID        string
	IdentityID      string
	UserID          string
	OrganizationIDs []string
	ProjectIDs      []string
	AllowAll        bool
	AllowSelf       bool
	// AssignedItemsOnly 是面向操作受理人的应用级业务规则，
	// 防止宽泛的平台应用范围使组长、
	// 项目经理或工程师成为租户全范围项目读取者。
	AssignedItemsOnly bool
}

func (p Principal) ProjectScopeFilter() (ScopeFilter, error) {
	filter := ScopeFilter{TenantID: strings.TrimSpace(p.TenantID), IdentityID: strings.TrimSpace(p.IdentityID), UserID: strings.TrimSpace(p.UserID)}
	if filter.IdentityID == "" {
		filter.IdentityID = filter.UserID
	}
	if filter.UserID == "" {
		filter.UserID = filter.IdentityID
	}
	orgs := map[string]struct{}{}
	projects := map[string]struct{}{}
	for _, scope := range p.DataScopes {
		switch scope.ScopeType {
		case "APPLICATION", "ENVIRONMENT", "TENANT":
			filter.AllowAll = true
		case "SELF":
			filter.AllowSelf = true
		case "ORG":
			orgs[scope.ScopeID] = struct{}{}
		case "PROJECT":
			projects[scope.ScopeID] = struct{}{}
		}
	}
	for value := range orgs {
		filter.OrganizationIDs = append(filter.OrganizationIDs, value)
	}
	for value := range projects {
		filter.ProjectIDs = append(filter.ProjectIDs, value)
	}
	// 负责人类角色的数据边界来自实际指派关系，而不是平台授予的应用入口范围。
	// 管理/治理角色仍按平台数据范围工作；混合角色只要含治理角色就保留其更高范围。
	if p.assignmentScopedOnly() {
		filter.AllowAll = false
		filter.AllowSelf = true
		filter.OrganizationIDs = nil
		filter.ProjectIDs = nil
		filter.AssignedItemsOnly = true
	}
	sort.Strings(filter.OrganizationIDs)
	sort.Strings(filter.ProjectIDs)
	if filter.TenantID == "" || (!filter.AllowAll && !filter.AllowSelf && len(filter.OrganizationIDs) == 0 && len(filter.ProjectIDs) == 0) {
		return ScopeFilter{}, ErrDataScopeDenied
	}
	return filter, nil
}

func (p Principal) assignmentScopedOnly() bool {
	roles := make(map[string]struct{}, len(p.Roles)+len(p.DataScopes))
	for _, role := range p.Roles {
		roles[strings.ToLower(strings.TrimSpace(role))] = struct{}{}
	}
	for _, scope := range p.DataScopes {
		roles[strings.ToLower(strings.TrimSpace(scope.RoleCode))] = struct{}{}
	}
	for _, role := range []string{"admin", "system_admin", "business_admin", "technical_director", "quality_manager", "device_admin"} {
		if _, ok := roles[role]; ok {
			return false
		}
	}
	for _, role := range []string{"team_lead", "project_manager", "engineer", "penetration_engineer"} {
		if _, ok := roles[role]; ok {
			return true
		}
	}
	return false
}

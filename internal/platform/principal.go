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

func (p Principal) HasApplicationScope() bool {
	return len(p.DataScopes) > 0
}

// ScopeFilter is the only business-data boundary accepted by repositories.
// The platform currently exposes a tenant-wide application grant as
// APPLICATION and an environment-wide grant as ENVIRONMENT, so both are
// explicit full-data scopes after the authorization-context binding checks.
type ScopeFilter struct {
	TenantID        string
	IdentityID      string
	OrganizationIDs []string
	ProjectIDs      []string
	AllowAll        bool
	AllowSelf       bool
}

func (p Principal) ProjectScopeFilter() (ScopeFilter, error) {
	filter := ScopeFilter{TenantID: strings.TrimSpace(p.TenantID), IdentityID: strings.TrimSpace(p.IdentityID)}
	if filter.IdentityID == "" {
		filter.IdentityID = strings.TrimSpace(p.UserID)
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
	sort.Strings(filter.OrganizationIDs)
	sort.Strings(filter.ProjectIDs)
	if filter.TenantID == "" || (!filter.AllowAll && !filter.AllowSelf && len(filter.OrganizationIDs) == 0 && len(filter.ProjectIDs) == 0) {
		return ScopeFilter{}, ErrDataScopeDenied
	}
	return filter, nil
}

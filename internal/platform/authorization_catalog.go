package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/j-s-te/project-management/authz"
)

type AuthorizationCatalog struct {
	Version     string
	roles       map[string]struct{}
	permissions map[string]struct{}
}

func LoadAuthorizationCatalog() (AuthorizationCatalog, error) {
	var manifest struct {
		CatalogVersion string `json:"catalog_version"`
		Permissions    []struct {
			Code string `json:"code"`
		} `json:"permissions"`
		Roles []struct {
			Code string `json:"code"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		return AuthorizationCatalog{}, fmt.Errorf("decode project permission catalog: %w", err)
	}
	result := AuthorizationCatalog{Version: strings.TrimSpace(manifest.CatalogVersion), roles: map[string]struct{}{}, permissions: map[string]struct{}{}}
	if result.Version == "" {
		return AuthorizationCatalog{}, errors.New("project permission catalog version is empty")
	}
	for _, item := range manifest.Roles {
		if item.Code == "" || item.Code != strings.TrimSpace(item.Code) {
			return AuthorizationCatalog{}, errors.New("project role catalog is malformed")
		}
		result.roles[item.Code] = struct{}{}
	}
	for _, item := range manifest.Permissions {
		if item.Code == "" || item.Code == "all" || item.Code != strings.TrimSpace(item.Code) {
			return AuthorizationCatalog{}, errors.New("project permission catalog is malformed")
		}
		result.permissions[item.Code] = struct{}{}
	}
	return result, nil
}

func (c AuthorizationCatalog) Validate(value AuthorizationContext, expectedClientID, expectedApplication, expectedEnvironment string) error {
	if strings.TrimSpace(value.Subject) == "" || strings.TrimSpace(value.IdentityID) == "" || value.IdentityID != strings.TrimSpace(value.IdentityID) || strings.TrimSpace(value.TenantID) == "" || value.AuthorizationRevision == 0 {
		return fmt.Errorf("%w: identity or revision", ErrInvalidAuthorization)
	}
	if value.ClientID != expectedClientID || value.ApplicationCode != expectedApplication || value.EnvironmentCode != expectedEnvironment {
		return fmt.Errorf("%w: client, application or environment binding", ErrInvalidAuthorization)
	}
	if value.CatalogVersion == "" && len(value.CompatibleCatalogVersions) == 0 && (value.RoleConfigHash != "" || len(value.CompatibleRoleConfigHashes) != 0) {
		return fmt.Errorf("%w: partial catalog compatibility response", ErrInvalidAuthorization)
	}
	if err := validateProjectCatalogCompatibility(c.Version, value.CatalogVersion, value.CompatibleCatalogVersions); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAuthorization, err)
	}
	roles, err := validateKnownSet(value.Roles, c.roles, false)
	if err != nil || len(roles) == 0 {
		return fmt.Errorf("%w: roles", ErrInvalidAuthorization)
	}
	if _, err := validateKnownSet(value.Permissions, c.permissions, true); err != nil || len(value.Permissions) == 0 {
		return fmt.Errorf("%w: permissions", ErrInvalidAuthorization)
	}
	if len(value.DataScopes) == 0 {
		return fmt.Errorf("%w: data scopes are empty", ErrInvalidAuthorization)
	}
	seen := map[string]struct{}{}
	for _, scope := range value.DataScopes {
		if scope.RoleCode == "" || scope.RoleCode != strings.TrimSpace(scope.RoleCode) {
			return fmt.Errorf("%w: data scope role", ErrInvalidAuthorization)
		}
		if _, ok := roles[scope.RoleCode]; !ok {
			return fmt.Errorf("%w: data scope role is not effective", ErrInvalidAuthorization)
		}
		if scope.ScopeType != strings.ToUpper(strings.TrimSpace(scope.ScopeType)) {
			return fmt.Errorf("%w: data scope type", ErrInvalidAuthorization)
		}
		switch scope.ScopeType {
		case "APPLICATION":
			if scope.EnvironmentCode != "" {
				return fmt.Errorf("%w: application scope environment", ErrInvalidAuthorization)
			}
		case "ENVIRONMENT":
			if scope.ScopeID == "" || scope.ScopeID != strings.TrimSpace(scope.ScopeID) || scope.EnvironmentCode != expectedEnvironment {
				return fmt.Errorf("%w: environment scope", ErrInvalidAuthorization)
			}
		case "TENANT":
			if scope.EnvironmentCode != "" && scope.EnvironmentCode != expectedEnvironment {
				return fmt.Errorf("%w: tenant scope environment", ErrInvalidAuthorization)
			}
		case "ORG", "PROJECT", "SELF":
			if scope.EnvironmentCode != "" && scope.EnvironmentCode != expectedEnvironment {
				return fmt.Errorf("%w: resource scope environment", ErrInvalidAuthorization)
			}
			if scope.ScopeID == "" || scope.ScopeID != strings.TrimSpace(scope.ScopeID) {
				return fmt.Errorf("%w: data scope id", ErrInvalidAuthorization)
			}
		default:
			return fmt.Errorf("%w: unsupported data scope type", ErrInvalidAuthorization)
		}
		if scope.ScopeType == "TENANT" && scope.ScopeID != "" && scope.ScopeID != value.TenantID {
			return fmt.Errorf("%w: tenant data scope", ErrInvalidAuthorization)
		}
		if scope.ScopeType == "SELF" && scope.ScopeID != value.IdentityID && (value.PersonID == "" || scope.ScopeID != value.PersonID) {
			return fmt.Errorf("%w: self data scope", ErrInvalidAuthorization)
		}
		key := scope.RoleCode + "\x00" + scope.ScopeType + "\x00" + scope.ScopeID + "\x00" + scope.EnvironmentCode
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate data scope", ErrInvalidAuthorization)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateProjectCatalogCompatibility(localVersion, currentVersion string, compatible []string) error {
	localVersion, currentVersion = strings.TrimSpace(localVersion), strings.TrimSpace(currentVersion)
	if currentVersion == "" && len(compatible) == 0 {
		return nil
	}
	if localVersion == "" || currentVersion == "" || len(compatible) == 0 || len(compatible) > 2 {
		return errors.New("catalog compatibility window is incomplete")
	}
	found, currentFound := false, false
	seen := map[string]struct{}{}
	for _, version := range compatible {
		if version == "" || version != strings.TrimSpace(version) {
			return errors.New("catalog compatibility version is not canonical")
		}
		if _, duplicate := seen[version]; duplicate {
			return errors.New("catalog compatibility window contains duplicates")
		}
		seen[version] = struct{}{}
		found = found || version == localVersion
		currentFound = currentFound || version == currentVersion
	}
	if !found || !currentFound {
		return errors.New("local authorization catalog is outside the N/N-1 compatibility window")
	}
	return nil
}

func validateKnownSet(values []string, known map[string]struct{}, rejectAll bool) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || rejectAll && value == "all" {
			return nil, errors.New("malformed value")
		}
		if _, ok := known[value]; !ok {
			return nil, errors.New("unknown value")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("duplicate value")
		}
		seen[value] = struct{}{}
	}
	return seen, nil
}

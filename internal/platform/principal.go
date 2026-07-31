package platform

type Principal struct {
	TenantID       string
	UserID         string
	DisplayName    string
	Roles          []string
	Permissions    map[string]bool
	RoleConfigHash string
	AuthzRevision  uint64
}

func (p Principal) Has(permission string) bool { return p.Permissions[permission] }

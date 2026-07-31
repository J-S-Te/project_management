package platform

import "testing"

func validClaims() oidcClaims {
	return oidcClaims{Subject: "user-1", TenantID: "tenant-1", Roles: []string{"project_manager"}, Permissions: []string{"project.read", "service_item.confirm"}, RoleConfigHash: "hash-1", AuthzRevision: 1}
}

func TestPrincipalFromClaims(t *testing.T) {
	principal, err := principalFromClaims(validClaims(), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != "user-1" || !principal.Has("project.read") {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestPrincipalRejectsWrongTenantAndWildcard(t *testing.T) {
	if _, err := principalFromClaims(validClaims(), "tenant-2"); err == nil {
		t.Fatal("wrong tenant was accepted")
	}
	claims := validClaims()
	claims.Permissions = []string{"all"}
	if _, err := principalFromClaims(claims, "tenant-1"); err == nil {
		t.Fatal("wildcard permission was accepted")
	}
}

func TestPrincipalRejectsMalformedAuthorizationMetadata(t *testing.T) {
	claims := validClaims()
	claims.Roles = []string{" project_manager "}
	if _, err := principalFromClaims(claims, "tenant-1"); err == nil {
		t.Fatal("malformed role was accepted")
	}
	claims = validClaims()
	claims.AuthzRevision = 0
	if _, err := principalFromClaims(claims, "tenant-1"); err == nil {
		t.Fatal("missing revision was accepted")
	}
}

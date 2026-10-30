package platform

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// TestImportProjectBindingScopeConflict 核验真实签名令牌的专用scope、租户和调用环境。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingScopeConflict(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &platformApplicationTokenVerifier{publicKey: public, issuer: "basic-platform", audience: "project-api", clientID: "contract-import-client", tenantID: "tenant", applicationCode: "contract_management", environmentCode: "qa", requiredScope: "project.contract.import"}
	now := time.Now().UTC()
	valid := platformApplicationTokenClaims{Issuer: "basic-platform", Audience: "project-api", TokenUse: "application", Subject: "contract-import-client", OAuthClientID: "oauth-client", TenantID: "tenant", ApplicationCode: "contract_management", EnvironmentCode: "qa", Scopes: []string{"project.contract.import"}, IssuedAt: now.Add(-time.Second).Unix(), NotBefore: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(time.Minute).Unix()}
	for _, scenario := range []string{"valid", "scope", "tenant", "environment", "caller", "audience", "client"} {
		claims := valid
		switch scenario {
		case "scope":
			claims.Scopes = []string{"dashboard.project.read"}
		case "tenant":
			claims.TenantID = "other"
		case "environment":
			claims.EnvironmentCode = "prod"
		case "caller":
			claims.ApplicationCode = "settlement"
		case "audience":
			claims.Audience = "settlement-api"
		case "client":
			claims.Subject = "other-client"
		}
		_, err := verifier.VerifyClientCredentials(context.Background(), signPlatformToken(t, private, claims))
		if scenario == "valid" && err != nil || scenario != "valid" && err == nil {
			t.Fatalf("scenario=%s err=%v", scenario, err)
		}
	}
}

// TestNormalProjectContractOriginPolicy 验证导入和未知来源不会沿普通目录重新建项。
// 参数：t 为测试上下文。
// 返回值：无。
func TestNormalProjectContractOriginPolicy(t *testing.T) {
	for _, item := range []ApprovedContract{{OriginKind: "IMPORTED"}, {ImportMode: "ARCHIVE_ONLY"}, {ImportMode: "CONTINUE_LIVE"}, {OriginImportItemID: "item"}, {OriginKind: "UNKNOWN"}} {
		if NormalProjectContract(item) {
			t.Fatalf("historical or unknown origin accepted: %+v", item)
		}
	}
	if !NormalProjectContract(ApprovedContract{}) || !NormalProjectContract(ApprovedContract{OriginKind: "NORMAL", ImportMode: "NORMAL"}) {
		t.Fatal("ordinary compatibility broken")
	}
}

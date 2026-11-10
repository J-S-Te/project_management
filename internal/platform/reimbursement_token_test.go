package platform

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// TestReimbursementTokenRequiresExactTrustBindings 验证报销独立ClientID、来源与单scope不能混用。
// 参数：t 为测试上下文。
// 返回值：无；任一错误边界被接受时测试失败。
func TestReimbursementTokenRequiresExactTrustBindings(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	opts := ClientCredentialsVerifierOptions{Issuer: "https://platform.test", Audience: "project-internal", PublicKeyPath: writeApplicationPublicKey(t, pub), ClientID: "reimbursement-read", TenantID: "t", CallerApplicationCode: "reimbursement", CallerEnvironmentCode: "test", RequiredScope: "reimbursement.project_reference.read"}
	raw, err := NewReimbursementTokenVerifier(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	v := raw.(*platformApplicationTokenVerifier)
	now := time.Now().UTC()
	valid := platformApplicationTokenClaims{Issuer: opts.Issuer, Audience: opts.Audience, TokenUse: "application", Subject: opts.ClientID, OAuthClientID: "registered-id", TenantID: "t", ApplicationCode: "reimbursement", EnvironmentCode: "test", Scopes: []string{opts.RequiredScope}, IssuedAt: now.Add(-time.Minute).Unix(), NotBefore: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Minute).Unix()}
	if _, err := raw.VerifyClientCredentials(context.Background(), signPlatformToken(t, private, valid)); err != nil {
		t.Fatal(err)
	}
	if !v.validClaims(valid, now) {
		t.Fatal("valid binding rejected")
	}
	changes := []func(*platformApplicationTokenClaims){func(c *platformApplicationTokenClaims) { c.Subject = "another-client" }, func(c *platformApplicationTokenClaims) { c.TenantID = "other" }, func(c *platformApplicationTokenClaims) { c.ApplicationCode = "settlement" }, func(c *platformApplicationTokenClaims) { c.EnvironmentCode = "prod" }, func(c *platformApplicationTokenClaims) { c.Audience = "another" }, func(c *platformApplicationTokenClaims) { c.Issuer = "another" }, func(c *platformApplicationTokenClaims) { c.TokenUse = "id_token" }, func(c *platformApplicationTokenClaims) {
		c.Scopes = []string{opts.RequiredScope, "project.reimbursement_cost.write"}
	}, func(c *platformApplicationTokenClaims) { c.ExpiresAt = now.Add(-time.Second).Unix() }}
	for i, change := range changes {
		candidate := valid
		change(&candidate)
		if _, err := raw.VerifyClientCredentials(context.Background(), signPlatformToken(t, private, candidate)); err == nil {
			t.Fatalf("signed invalid binding %d accepted", i)
		}
		if v.validClaims(candidate, now) {
			t.Fatalf("binding %d accepted", i)
		}
	}
	_, foreign, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := raw.VerifyClientCredentials(context.Background(), signPlatformToken(t, foreign, valid)); err == nil {
		t.Fatal("foreign signature accepted")
	}
	opts.AllowAnyClientSubject = true
	if _, err := NewReimbursementTokenVerifier(context.Background(), opts); err == nil {
		t.Fatal("any-client option accepted")
	}
}

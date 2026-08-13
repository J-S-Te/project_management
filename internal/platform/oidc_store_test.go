package platform

import (
	"bytes"
	"testing"
)

func TestOIDCSecretCodecUsesRandomAuthenticatedEncryption(t *testing.T) {
	codec, err := newSecretCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("refresh-token-secret")
	first, err := codec.encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := codec.encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, plaintext) || bytes.Equal(first, second) {
		t.Fatal("OIDC secret encryption is deterministic or stored plaintext")
	}
	decoded, err := codec.decrypt(first)
	if err != nil || !bytes.Equal(decoded, plaintext) {
		t.Fatalf("decoded=%q error=%v", decoded, err)
	}
}

func TestOIDCCookieAndStateDigestAreNotRawValues(t *testing.T) {
	raw := "browser-secret-value"
	digest := tokenDigest(raw)
	if len(digest) != 32 || bytes.Equal(digest, []byte(raw)) {
		t.Fatalf("digest=%x", digest)
	}
}

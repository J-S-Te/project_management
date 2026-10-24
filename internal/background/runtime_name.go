package background

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// runtimeName keeps all 256 bits of the trusted identity tuple, including its
// configurable base. Temporal 1.29 SQL prepends namespace/type and base64-encodes
// deployment and Build inside a physical queue name (VARCHAR/VARBINARY255).
// Long readable prefixes plus hex identities exceed that limit even when each
// public API name is individually valid. Builds retain their full image digest.
func runtimeName(kind, base, environment, tenant string) string {
	data, _ := json.Marshal([]string{"project-background/runtime/v1", kind, base, environment, tenant})
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

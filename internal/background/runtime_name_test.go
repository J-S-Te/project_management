package background

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
)

func TestRuntimeNamesFitTemporalMySQLWithoutTruncatingIdentity(t *testing.T) {
	o := validOptions()
	o.TenantID = strings.Repeat("t", 64)
	o.Environment = strings.Repeat("e", 128)
	o.TaskQueue = strings.Repeat("q", 128)
	o.DeploymentName = strings.Repeat("d", 128)
	o.BuildID = "project-background-sha256-" + strings.Repeat("a", 64)
	makeRuntime := func(v Options) *Runtime {
		t.Helper()
		r, e := NewRuntime(v, &application.Service{CheckBusinessLicense: func(context.Context) error { return nil }}, &runtimeClient{}, nil)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := makeRuntime(o)
	for _, name := range []string{r.taskQueue, r.deployment} {
		decoded, e := base64.RawURLEncoding.DecodeString(name)
		if e != nil || len(decoded) != 32 {
			t.Fatalf("runtime name is not a full 256-bit compact identity: length=%d", len(name))
		}
	}
	physical := "/_sys/" + r.taskQueue + "/" + base64.RawURLEncoding.EncodeToString([]byte(r.deployment)) + "|" + base64.RawURLEncoding.EncodeToString([]byte(o.BuildID)) + "#" + fmt.Sprint(int64(99999999))
	// Temporal SQL prepends the 16-byte namespace UUID and one task type byte.
	// Reserve eight partition digits; the delivered server uses four partitions.
	if size := 17 + len(physical); size > 255 {
		t.Fatalf("Temporal MySQL task_queue_id needs %d bytes, maximum255", size)
	}
	if r.taskQueue == r.deployment {
		t.Fatal("queue and deployment identities lack domain separation")
	}
	for _, change := range []func(*Options){func(v *Options) { v.TenantID = "other" }, func(v *Options) { v.Environment = "other" }, func(v *Options) { v.TaskQueue = "other" }, func(v *Options) { v.DeploymentName = "other" }} {
		v := o
		change(&v)
		other := makeRuntime(v)
		if other.taskQueue == r.taskQueue && other.deployment == r.deployment {
			t.Fatal("trusted identity input was ignored")
		}
	}
	v := o
	v.BuildID = "project-background-sha256-" + strings.Repeat("b", 64)
	other := makeRuntime(v)
	if other.taskQueue != r.taskQueue || other.deployment != r.deployment {
		t.Fatal("build upgrade changed owned runtime names")
	}
}

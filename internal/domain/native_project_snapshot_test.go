package domain

import (
	"encoding/json"
	"testing"
	"time"
)

// TestNativeProjectSnapshotPreservesSchemaOne 验证原生项目不改变既有严格schema1消费者契约。
// 参数：t 为测试上下文。
// 返回值：无；出现伪合同或新增字段时标记失败。
func TestNativeProjectSnapshotPreservesSchemaOne(t *testing.T) {
	now := time.Now().UTC()
	projection := NewProjectProjection(Project{ID: "PJ-NATIVE", TenantID: "tenant", SourceKind: "NATIVE", SourceID: "request-1", ScopeVersion: 1,
		CustomerID: "local-unit-1", Name: "原生项目", CreatedAt: now, UpdatedAt: now}, "snapshot", nil, now)
	data, err := ProjectSnapshotLine(projection)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["contract_id"] != "" || fields["contract"] != "" {
		t.Fatalf("invented contract=%s", data)
	}
	for _, key := range []string{"source_kind", "source_id", "scope_version"} {
		if _, exists := fields[key]; exists {
			t.Fatalf("schema1 incompatible field %s", key)
		}
	}
	// report_stage 是 schema1 内的增量键：原生项目无服务项，恒为空串而不是缺失/nulls。
	if stage, exists := fields["report_stage"]; !exists || stage != "" {
		t.Fatalf("native report_stage=%v exists=%v wire=%s", stage, exists, data)
	}
}

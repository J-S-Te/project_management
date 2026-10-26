package mysql

import (
	"os"
	"strings"
	"testing"
)

// 统一审计方案 §3.1：建项是关键业务变更，必须与项目行同事务写 prj_audit_outbox。
func TestCreateProjectWritesAuditOutbox(t *testing.T) {
	source, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("读取 repository.go: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (r *Repository) CreateProject(")
	if start < 0 {
		t.Fatal("未找到 CreateProject")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		t.Fatal("未找到 CreateProject 结束位置")
	}
	body := text[start : start+end]
	if !strings.Contains(body, "PROJECT_MANAGEMENT:PROJECT_CREATED") {
		t.Fatalf("CreateProject 必须写建项审计：%s", body)
	}
	if !strings.Contains(body, "auditOutboxRecord") || !strings.Contains(body, ".Transaction(") {
		t.Fatalf("建项审计必须与业务同事务写入 outbox：%s", body)
	}
	// 审计行不得携带合同正文或客户联系方式。
	for _, forbidden := range []string{"Contract:", "Customer:", "Manager:"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("建项审计不得写入 %s", forbidden)
		}
	}
}

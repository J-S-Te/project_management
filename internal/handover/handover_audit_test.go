package handover

import (
	"os"
	"strings"
	"testing"
)

// 统一审计方案 §3.1：离职交接变更项目责任人属于关键业务变更，
// 必须与责任变更同事务写审计，且标注为系统执行（交接批次驱动）。
func TestTransferOwnershipWritesHandoverAudit(t *testing.T) {
	source, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("读取 repository.go: %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "PROJECT_MANAGEMENT:HANDOVER_TRANSFERRED") {
		t.Fatal("交接缺少审计事件")
	}
	start := strings.Index(text, "func (repository *GORMRepository) TransferOwnership(")
	end := strings.Index(text[start:], "\n}\n")
	body := text[start : start+end]
	if !strings.Contains(body, "writeHandoverAudit(tx, in)") {
		t.Fatalf("交接审计必须与责任变更同事务：%s", body)
	}
	if !strings.Contains(text, `ActorType: "SYSTEM"`) {
		t.Fatal("交接由平台批次驱动，必须标注 SYSTEM")
	}
}

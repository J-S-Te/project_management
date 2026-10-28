package httpapi

import (
	core "github.com/J-S-Te/license-core"
	"net/http"
	"testing"
)

// TestSettlementReferenceIsExactHistoryRead 验证项目引用仅精确GET获得历史读取许可。
// 参数：t 为测试上下文。
// 返回值：无。
func TestSettlementReferenceIsExactHistoryRead(t *testing.T) {
	path := "/internal/v1/settlement/project-references/:projectID"
	if commercialOperation(http.MethodGet, path) != core.READ_HISTORY {
		t.Fatal("权威引用未走只读许可")
	}
	if commercialOperation(http.MethodPost, path) != core.MUTATE_BUSINESS {
		t.Fatal("引用写入被错误授予只读许可")
	}
}

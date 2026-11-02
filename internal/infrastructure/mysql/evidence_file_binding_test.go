package mysql

import (
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
)

// TestEvidenceFileReuseIsRejectedAcrossServiceItems 覆盖 BIZ-PROJECT-03 的台账侧防线：
// pm_evidence_file 的唯一键是 (tenant_id, file_id)，同一文件只能归属一个服务项。
// 已被其它服务项登记的文件必须映射成可解释的 422，而不是让 MySQL 1062 兜底成 500。
func TestEvidenceFileReuseIsRejectedAcrossServiceItems(t *testing.T) {
	if err := evidenceFileReuseError("SI-A", "SI-A"); err != nil {
		t.Fatalf("same service item reuse error = %v, want nil (idempotent)", err)
	}
	err := evidenceFileReuseError("SI-A", "SI-B")
	if !errors.Is(err, application.ErrValidation) {
		t.Fatalf("cross service item reuse error = %v, want ErrValidation", err)
	}
}

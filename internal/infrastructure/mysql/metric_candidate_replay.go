package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

// ReplayMetricUsageCandidate 用途：还原已提交候选的精确回执，避免现场事件增加后原键失效。
// 参数：ctx/filter 为可信范围，actor 为当前人，item 为可见父项，in 为原外部命令。
// 返回值：原回执和命中标志；键被其他动作或主体使用时冲突，不返回其他事实。
func (r *Repository) ReplayMetricUsageCandidate(ctx context.Context, filter platform.ScopeFilter, actor string, item domain.ServiceItem, in domain.MetricUsageCandidateApplyInput) (domain.ResourceUsage, bool, error) {
	var operation metricOperationRecord
	err := r.db.WithContext(ctx).Where("tenant_id=? AND operation_key=?", filter.TenantID, in.IdempotencyKey).Take(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ResourceUsage{}, false, nil
	}
	if err != nil {
		return domain.ResourceUsage{}, false, err
	}
	var out domain.ResourceUsage
	if json.Unmarshal(operation.ResultJSON, &out) != nil || out.ID == "" || out.Version == 0 ||
		out.CreatedBy != actor || out.ServiceItemID != item.ID || out.ProjectID != item.ProjectID ||
		out.ResourceID != in.ResourceID || out.BusinessDate != in.BusinessDate ||
		out.ResourceType != "PERSON" || out.Source != "FIELD_EVENTS" || len(out.SourceEventIDs) == 0 {
		return domain.ResourceUsage{}, false, application.ErrConflict
	}
	original := domain.ResourceUsageInput{ResourceType: out.ResourceType, ResourceID: out.ResourceID,
		ServiceItemID: out.ServiceItemID, BusinessDate: out.BusinessDate, Start: out.Start, End: out.End,
		Note: out.Note, Evidence: out.Evidence, CorrectsID: out.CorrectsID, Source: out.Source,
		SourceRef: out.SourceRef, SourceEventIDs: out.SourceEventIDs, AutoConfirm: out.AutoConfirmed,
		IdempotencyKey: in.IdempotencyKey}
	body, err := json.Marshal([]any{"USAGE_CREATE", actor, original})
	if err != nil {
		return domain.ResourceUsage{}, false, err
	}
	digest := sha256.Sum256(body)
	if operation.Fingerprint != hex.EncodeToString(digest[:]) {
		return domain.ResourceUsage{}, false, application.ErrConflict
	}
	out.TenantID = filter.TenantID
	return out, true, nil
}

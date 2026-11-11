package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/auditctx"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type metricReviewRecord struct{ domain.ReportReviewRound }

// TableName 指定审核事实表。
// 参数：无。
// 返回值：固定业务表名。
func (metricReviewRecord) TableName() string { return "pm_report_review_round" }

type metricCapacityRecord struct{ domain.ResourceCapacity }

// TableName 指定容量事实表。
// 参数：无。
// 返回值：固定业务表名。
func (metricCapacityRecord) TableName() string { return "pm_resource_capacity" }

type metricUsageRecord struct{ domain.ResourceUsage }

// TableName 指定实际用量表。
// 参数：无。
// 返回值：固定业务表名。
func (metricUsageRecord) TableName() string { return "pm_resource_usage" }

type metricOperationRecord struct {
	TenantID     string `gorm:"primaryKey;size:64"`
	OperationKey string `gorm:"primaryKey;size:128"`
	Fingerprint  string `gorm:"size:64"`
	ResultJSON   []byte `gorm:"type:json"`
	CreatedAt    time.Time
}

// TableName 指定不可变幂等操作表。
// 参数：无。
// 返回值：固定业务表名。
func (metricOperationRecord) TableName() string { return "pm_metric_operation" }

// metricOperation 在已锁定业务主体的事务内识别相同操作重放。
// 参数：tx 为写事务，tenant/key 为幂等身份，input 包含动作、操作者及业务载荷，out 接收原结果。
// 返回值：是否重放和稳定指纹；同键异载荷拒绝，查询失败原样返回。
func metricOperation(tx *gorm.DB, tenant, key string, input, out any) (bool, string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return false, "", err
	}
	sum := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(sum[:])
	var row metricOperationRecord
	err = tx.Where("tenant_id=? AND operation_key=?", tenant, key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, fingerprint, nil
	}
	if err != nil {
		return false, "", err
	}
	if row.Fingerprint != fingerprint {
		return false, "", application.ErrConflict
	}
	return true, fingerprint, json.Unmarshal(row.ResultJSON, out)
}

// saveMetricOperation 同事务保存幂等结果与审计意图。
// 参数：tx 为写事务，tenant/key/hash 为幂等信息，actor/action/id 为审计身份，out 为返回事实。
// 返回值：任一落库失败即回滚；幂等键并发冲突作为业务冲突返回。
func saveMetricOperation(tx *gorm.DB, tenant, key, hash, actor, action, id string, out any) error {
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	err = tx.Create(&metricOperationRecord{TenantID: tenant, OperationKey: key, Fingerprint: hash, ResultJSON: body, CreatedAt: now}).Error
	if isDuplicateKey(err) {
		return application.ErrConflict
	}
	if err != nil {
		return err
	}
	meta := auditctx.FromContext(tx.Statement.Context)
	return tx.Create(&auditOutboxRecord{ID: ulid.Make().String(), TenantID: tenant, EventID: ulid.Make().String(), ActorID: actorPointer(actor), ActorType: "USER",
		Action: "PROJECT_MANAGEMENT:METRIC_" + action, ResourceType: "metric_fact", ResourceID: id, RequestID: auditRequestID(meta.RequestID), LoginIP: auditIPBytes(meta.LoginIP), ClientIP: auditIPBytes(meta.ClientIP), IPSource: auditIPSource(meta.IPSource), Result: "SUCCESS", DeliveryStatus: "pending", NextAttemptAt: now, CreatedAt: now}).Error
}

// metricReportIssueGate 拒绝新流程报告绕过最新轮次的独立通过结论签发。
// 参数：tx 为已锁定报告事务，revision 为当前版本。
// 返回值：历史报告兼容；新报告无真实通过轮次时返回前置条件错误。
func metricReportIssueGate(tx *gorm.DB, revision *reportRevisionRecord) error {
	if !revision.ReviewRequired {
		return nil
	}
	var row metricReviewRecord
	err := tx.Where("tenant_id=? AND subject_type=? AND subject_id=? AND revision=?", revision.TenantID, revision.SubjectType, revision.SubjectID, revision.Revision).Order("round_no DESC").Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if row.Decision != "PASSED" || row.DecidedBy == "" || row.DecidedBy == row.PreparedBy || row.DecidedBy == row.SubmittedBy {
		return application.PreconditionError("当前报告须经独立审核通过后才能签发")
	}
	return nil
}

// MetricReportReview 在父服务项锁内提交或决定当前报告的真实审核轮次。
// 参数：ctx 为请求，filter 为可信项目范围，itemID 为父项，actor 为实际用户，in 为提交或决定。
// 返回值：不可变审核事实；旧版、重复决定、自审或越界时拒绝。
func (r *Repository) MetricReportReview(ctx context.Context, filter platform.ScopeFilter, itemID, actor string, in domain.ReportReviewInput) (domain.ReportReviewRound, error) {
	var out domain.ReportReviewRound
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item serviceItemRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", filter.TenantID, itemID).Take(&item).Error; err != nil {
			return mapNotFound(err)
		}
		scoped := &Repository{db: tx}
		if _, err := scoped.GetServiceItem(ctx, filter, itemID); err != nil {
			return err
		}
		var project projectRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", filter.TenantID, item.ProjectID).Take(&project).Error; err != nil {
			return mapNotFound(err)
		}
		replay, hash, err := metricOperation(tx, filter.TenantID, in.IdempotencyKey, []any{actor, itemID, in}, &out)
		if err != nil || replay {
			return err
		}
		revision, version, pkg, err := metricCurrentReport(tx, &item, in.SubjectType)
		if err != nil {
			return err
		}
		if revision.Revision != in.Revision || version != in.ExpectedVersion || revision.ValidityStatus != "ACTIVE" {
			return application.ErrConflict
		}
		if in.Decision == "" {
			err = metricSubmitReport(tx, &item, &project, &revision, actor, in, &out)
		} else {
			err = metricDecideReport(tx, &revision, actor, in, &out)
		}
		if err != nil {
			return err
		}
		phase := revision.Status
		if in.Decision == "PASSED" {
			phase = "REVIEWED"
			if pkg != nil {
				phase = "APPROVED"
			}
		}
		if pkg != nil {
			if in.Decision == "" {
				phase = "SUBMITTED"
			}
			if in.Decision == "RETURNED" {
				phase = "DRAFTING"
			}
			if err = tx.Model(&penetrationWorkPackageRecord{}).Where("tenant_id=? AND id=? AND version=?", filter.TenantID, pkg.ID, version).Updates(map[string]any{"report_status": phase, "version": version + 1, "updated_by": actor, "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]any{"report_status": phase, "version": version + 1, "report_updated_by": actor, "report_updated_at": time.Now().UTC()}
			if in.Decision == "PASSED" {
				updates["report_reviewed_by"] = actor
			}
			if err = tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND id=? AND version=?", filter.TenantID, item.ID, version).Updates(updates).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{"status": phase, "updated_at": time.Now().UTC()}
		if in.Decision == "PASSED" {
			updates["reviewed_by"] = actor
			updates["reviewed_at"] = out.DecidedAt
		}
		if err = tx.Model(&reportRevisionRecord{}).Where("id=?", revision.ID).Updates(updates).Error; err != nil {
			return err
		}
		return saveMetricOperation(tx, filter.TenantID, in.IdempotencyKey, hash, actor, "REPORT_REVIEW", out.ID, out)
	})
	return out, err
}

// metricCurrentReport 解析主报告或专项的当前有效版本与业务版本。
// 参数：tx 为父项锁内事务，item 为父项，subjectType 为明确报告类型。
// 返回值：当前版本、并发版本、可选专项；不存在或状态不允许时返回错误。
func metricCurrentReport(tx *gorm.DB, item *serviceItemRecord, subjectType string) (reportRevisionRecord, uint64, *penetrationWorkPackageRecord, error) {
	if subjectType == "SERVICE_ITEM" {
		if item.Status != "现场实施完成" {
			return reportRevisionRecord{}, 0, nil, application.ErrPrecondition
		}
		revision, err := lockReportRevision(tx, item)
		return revision, item.Version, nil, err
	}
	var pkg penetrationWorkPackageRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND parent_service_item_id=?", item.TenantID, item.ID).Take(&pkg).Error; err != nil {
		return reportRevisionRecord{}, 0, nil, mapNotFound(err)
	}
	if pkg.ExecutionStatus != "COMPLETED" {
		return reportRevisionRecord{}, 0, nil, application.ErrPrecondition
	}
	revision, err := lockPenetrationReportRevision(tx, &pkg, time.Now().UTC())
	return revision, pkg.Version, &pkg, err
}

// metricSubmitReport 冻结提交证据并开启退回后的下一轮审核。
// 参数：tx 为事务，item/project/revision 为可信主体，actor/in 为提交身份与输入，out 接收事实。
// 返回值：已有待审、已通过、无编制文件或不处于编制阶段时拒绝。
func metricSubmitReport(tx *gorm.DB, item *serviceItemRecord, project *projectRecord, revision *reportRevisionRecord, actor string, in domain.ReportReviewInput, out *domain.ReportReviewRound) error {
	if revision.Status != "COMPILING" && revision.Status != "DRAFTING" {
		return application.PreconditionError("只能提交当前编制中的报告")
	}
	if revision.PreparedBy == "" || revision.FileID == "" || revision.FileSHA256 == "" {
		return application.PreconditionError("请先完成报告编制和文件登记")
	}
	var previous metricReviewRecord
	err := tx.Where("tenant_id=? AND subject_type=? AND subject_id=? AND revision=?", item.TenantID, revision.SubjectType, revision.SubjectID, revision.Revision).Order("round_no DESC").Take(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil && previous.Decision != "RETURNED" {
		return application.ErrConflict
	}
	*out = domain.ReportReviewRound{FileID: revision.FileID, FileSHA256: revision.FileSHA256, ID: ulid.Make().String(), TenantID: item.TenantID, ProjectID: item.ProjectID, ServiceItemID: item.ID, SubjectType: revision.SubjectType, SubjectID: revision.SubjectID, Revision: revision.Revision, RoundNo: previous.RoundNo + 1, SourceVersion: in.ExpectedVersion,
		OrganizationID: project.OwnerOrgID, IdentityID: item.ProjectManagerID, PreparedBy: revision.PreparedBy, SubmittedBy: actor, SubmittedAt: time.Now().UTC(), Decision: "PENDING"}
	return tx.Create(&metricReviewRecord{*out}).Error
}

// metricDecideReport 仅允许异于提交和编制人的审核员终结当前待审轮次。
// 参数：tx 为报告锁内事务，revision 为当前版本，actor/in 为审核身份与决定，out 接收事实。
// 返回值：自审、旧轮次、非法退回原因或重复决定时拒绝。
func metricDecideReport(tx *gorm.DB, revision *reportRevisionRecord, actor string, in domain.ReportReviewInput, out *domain.ReportReviewRound) error {
	var row metricReviewRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND subject_type=? AND subject_id=? AND revision=?", revision.TenantID, revision.SubjectType, revision.SubjectID, revision.Revision).Order("round_no DESC").Take(&row).Error; err != nil {
		return mapNotFound(err)
	}
	if row.ID != in.RoundID || row.Decision != "PENDING" || row.FileID != revision.FileID || row.FileSHA256 != revision.FileSHA256 {
		return application.ErrConflict
	}
	if actor == row.PreparedBy || actor == row.SubmittedBy {
		return application.ErrForbidden
	}
	now := time.Now().UTC()
	row.Decision = in.Decision
	row.DecidedBy = actor
	row.DecidedAt = &now
	row.ReasonCode = in.ReasonCode
	row.Note = in.Note
	if err := tx.Save(&row).Error; err != nil {
		return err
	}
	*out = row.ReportReviewRound
	return nil
}

// ListMetricReportReviews 分页查询单个父项的真实审核历史。
// 参数：ctx 为请求，tenant/itemID 为已授权主体，page 为从1开始页码。
// 返回值：至多50个真实轮次；数据库失败时返回错误。
func (r *Repository) ListMetricReportReviews(ctx context.Context, tenant, itemID string, page int) ([]domain.ReportReviewRound, error) {
	var rows []metricReviewRecord
	err := r.db.WithContext(ctx).Where("tenant_id=? AND service_item_id=?", tenant, itemID).Order("submitted_at DESC,id DESC").Offset((page - 1) * 50).Limit(50).Find(&rows).Error
	out := make([]domain.ReportReviewRound, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ReportReviewRound)
	}
	return out, err
}

// metricArtifactGate 禁止新流程报告送审后替换原轮次文件。
// 参数：tx 为父项锁内事务，revision 为当前有效版本。
// 返回值：待审或已通过时拒绝，退回后允许编制者重新登记。
func metricArtifactGate(tx *gorm.DB, revision *reportRevisionRecord) error {
	if !revision.ReviewRequired {
		return nil
	}
	var count int64
	if err := tx.Model(&metricReviewRecord{}).Where("tenant_id=? AND subject_type=? AND subject_id=? AND revision=? AND decision IN ?", revision.TenantID, revision.SubjectType, revision.SubjectID, revision.Revision, []string{"PENDING", "PASSED"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return application.PreconditionError("报告已送审，当前文件已冻结；审核退回后才能重新登记")
	}
	return nil
}

// CurrentMetricReportReviews 在只读事务中分别查询主报告与专项当前版本最新轮次。
// 参数：ctx 为取消上下文，tenant/itemID 为已授权父项键。
// 返回值：最多两个当前轮次；不受任何历史分页或另一主体轮次数量影响。
func (r *Repository) CurrentMetricReportReviews(ctx context.Context, tenant, itemID string) ([]domain.ReportReviewRound, error) {
	out := []domain.ReportReviewRound{}
	tx := r.db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var item serviceItemRecord
	if err := tx.Where("tenant_id=? AND id=?", tenant, itemID).Take(&item).Error; err != nil {
		return nil, mapNotFound(err)
	}
	subjects := []struct {
		kind, id string
		revision uint64
	}{{"SERVICE_ITEM", item.ID, item.ReportRevision}}
	var pkg penetrationWorkPackageRecord
	err := tx.Where("tenant_id=? AND parent_service_item_id=?", tenant, itemID).Take(&pkg).Error
	if err == nil {
		subjects = append(subjects, struct {
			kind, id string
			revision uint64
		}{"PENETRATION_WORK_PACKAGE", pkg.ID, pkg.ReportRevision})
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	for _, subject := range subjects {
		var row metricReviewRecord
		err = tx.Where("tenant_id=? AND subject_type=? AND subject_id=? AND revision=?", tenant, subject.kind, subject.id, subject.revision).Order("round_no DESC").Take(&row).Error
		if err == nil {
			out = append(out, row.ReportReviewRound)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if err = tx.Commit().Error; err != nil {
		return nil, err
	}
	return out, nil
}

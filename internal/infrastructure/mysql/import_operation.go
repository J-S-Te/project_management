package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"gorm.io/gorm"
)

// importOperationRecord 是 pm_import_operation 的持久化形态：
// 每个「租户 + Idempotency-Key」一行，保存首次确认的请求指纹与完整响应快照。
type importOperationRecord struct {
	ID             string    `gorm:"column:id;primaryKey;size:32"`
	TenantID       string    `gorm:"column:tenant_id;size:64;not null"`
	IdempotencyKey string    `gorm:"column:idempotency_key;size:128;not null"`
	OperationType  string    `gorm:"column:operation_type;size:64;not null"`
	RequestHash    string    `gorm:"column:request_hash;type:char(64);not null"`
	ResponseJSON   string    `gorm:"column:response_json;type:longtext;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;not null"`
}

func (importOperationRecord) TableName() string { return "pm_import_operation" }

// errImportOperationRecorded 是并发同键的内部信号：本事务写入的台账撞上唯一键，
// 说明另一个事务已完成同一键的确认，本事务必须整体回滚后回读首次结果。
var errImportOperationRecorded = errors.New("import operation already recorded")

// RunImportConfirmation 在单个数据库事务内执行确认写入并登记幂等台账。
//   - write 返回错误时事务回滚：不产生任何部分写入，也不登记操作记录；
//   - 台账登记失败（同键并发）时回滚并读回已提交的记录，调用方按重放处理；
//   - 同键但请求指纹不同由应用层判定冲突，这里只负责稳定回读。
func (r *Repository) RunImportConfirmation(ctx context.Context, op application.ImportOperation, write func(application.ImportRowWriter) (application.ImportConfirmationResult, error)) (application.ImportOperation, bool, error) {
	if existing, found, err := r.findImportOperation(ctx, op.TenantID, op.IdempotencyKey); err != nil {
		return application.ImportOperation{}, false, err
	} else if found {
		return existing, true, nil
	}
	var stored application.ImportOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 事务作用域仓储：行写入与台账登记共享同一个事务与连接。
		scoped := &Repository{db: tx}
		result, writeErr := write(scoped)
		if writeErr != nil {
			return writeErr
		}
		payload, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return marshalErr
		}
		record := importOperationRecord{
			ID:             op.ID,
			TenantID:       op.TenantID,
			IdempotencyKey: op.IdempotencyKey,
			OperationType:  op.OperationType,
			RequestHash:    op.RequestHash,
			ResponseJSON:   string(payload),
			CreatedAt:      op.CreatedAt,
		}
		if createErr := tx.Create(&record).Error; createErr != nil {
			if isDuplicateKey(createErr) {
				return errImportOperationRecorded
			}
			return createErr
		}
		stored = importOperationFromRecord(record)
		return nil
	})
	if errors.Is(err, errImportOperationRecorded) {
		existing, found, readErr := r.findImportOperation(ctx, op.TenantID, op.IdempotencyKey)
		if readErr != nil {
			return application.ImportOperation{}, false, readErr
		}
		if !found {
			// 唯一键冲突但读不到记录：让调用方看到原始冲突，绝不返回"成功"。
			return application.ImportOperation{}, false, err
		}
		return existing, true, nil
	}
	if err != nil {
		// 写入阶段失败也可能来自并发同键的另一个事务：它已登记成功并释放了行锁，
		// 本次事务已整体回滚。此时回读首次结果按重放处理，不能把幂等重试报成 422/500。
		if existing, found, readErr := r.findImportOperation(ctx, op.TenantID, op.IdempotencyKey); readErr == nil && found {
			return existing, true, nil
		}
		return application.ImportOperation{}, false, err
	}
	return stored, false, nil
}

func (r *Repository) findImportOperation(ctx context.Context, tenantID, idempotencyKey string) (application.ImportOperation, bool, error) {
	var record importOperationRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND idempotency_key = ?", tenantID, idempotencyKey).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return application.ImportOperation{}, false, nil
	}
	if err != nil {
		return application.ImportOperation{}, false, err
	}
	return importOperationFromRecord(record), true, nil
}

func importOperationFromRecord(record importOperationRecord) application.ImportOperation {
	return application.ImportOperation{
		ID:             record.ID,
		TenantID:       record.TenantID,
		IdempotencyKey: record.IdempotencyKey,
		OperationType:  record.OperationType,
		RequestHash:    record.RequestHash,
		ResponseJSON:   record.ResponseJSON,
		CreatedAt:      record.CreatedAt,
	}
}

package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

// 确认导入的幂等与原子性边界。
//
// 旧实现确认时逐行独立保存：单行失败只累计跳过原因，其余行照常落库，且重试没有幂等身份，
// 同一份文件重复确认可能重复写入。这里把三个导入入口统一为：
//  1. 客户端必须提供 Idempotency-Key；服务端按「租户 + 键」登记一次确认结果；
//  2. 本次选中的全部行写入与登记记录在同一个数据库事务内完成，任一行失败整批回滚（全有或全无）；
//  3. 同键重放（request_hash 相同）返回首次响应，同键但请求指纹不同按冲突拒绝。
//
// 预检（preview）行为不变：仍然只读、不写台账、不消耗幂等键。

// ErrIdempotencyConflict 表示同一租户下该幂等键已登记过另一份导入请求。
// 它与 ErrConflict（行版本状态冲突）区分：调用方应重新预检并生成新的幂等键。
var ErrIdempotencyConflict = errors.New("idempotency key already used for a different import request")

// IdempotencyConflictError 构造可展示的幂等键冲突错误。
func IdempotencyConflictError(reason string) error {
	return ReasonError{Kind: ErrIdempotencyConflict, Reason: reason}
}

// 操作类型进入请求指纹与台账，保证三类导入即使误用同一个键也不会互相重放。
const (
	ImportOperationCapability        = "CAPABILITY_IMPORT"
	ImportOperationEquipment         = "EQUIPMENT_IMPORT"
	ImportOperationDetectionCategory = "DETECTION_CATEGORY_IMPORT"
)

// ImportOutcomeRow 是确认结果中的单行结论（成功 / 失败 / 未选择）。
type ImportOutcomeRow struct {
	RowNo   int    `json:"row_no"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ImportConfirmationResult 是确认导入的稳定响应。
// 首次成功响应会原样写入 pm_import_operation.response_json，同键重放必须逐字一致。
type ImportConfirmationResult struct {
	Imported int                `json:"imported"`
	Skipped  int                `json:"skipped"`
	Errors   []string           `json:"errors"`
	Rows     []ImportOutcomeRow `json:"rows"`
}

// ImportOperation 是一条已登记的确认导入记录。
type ImportOperation struct {
	ID             string
	TenantID       string
	IdempotencyKey string
	OperationType  string
	RequestHash    string
	ResponseJSON   string
	CreatedAt      time.Time
}

// ImportRowWriter 是确认事务内的写入面。实现方必须把每次调用放在同一个数据库事务里：
// 任一次返回错误，整批写入与幂等台账一起回滚，不允许留下部分数据。
type ImportRowWriter interface {
	UpsertCapability(context.Context, domain.Capability, string) (domain.Capability, error)
	CreateEquipmentForImport(context.Context, domain.Capability, string) (domain.Capability, error)
	SaveDetectionCategory(context.Context, string, domain.DetectionCategory, string) (domain.DetectionCategory, error)
}

// ImportConfirmationStore 以单事务执行确认写入并登记幂等记录。
// 返回值 (record, replayed, err)：replayed 为 true 表示该键已登记过，record 是首次结果。
type ImportConfirmationStore interface {
	RunImportConfirmation(ctx context.Context, op ImportOperation, write func(ImportRowWriter) (ImportConfirmationResult, error)) (ImportOperation, bool, error)
}

// NormalizeImportIdempotencyKey 校验客户端幂等键：去空白后长度 1..128，
// 字符集与项目既有的离线操作编号一致（字母、数字、点、下划线、冒号、连字符）。
func NormalizeImportIdempotencyKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", ValidationError("缺少 Idempotency-Key 请求头，已拒绝确认导入")
	}
	if len(key) > 128 {
		return "", ValidationError("Idempotency-Key 长度必须在 1 到 128 个字符之间")
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}
		return "", ValidationError("Idempotency-Key 只能包含字母、数字、点、下划线、冒号和连字符")
	}
	return key, nil
}

// ImportContentHash 返回导入文件的 SHA-256 十六进制指纹（按原始字节计算，含 BOM 与换行）。
func ImportContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ImportRequestHash 生成确认导入的稳定请求指纹：
// 操作类型 + 文件内容 SHA-256 + 资源类型 + 选中行集合（升序去重）。
// 同一份文件、同一组选中行无论提交顺序如何都得到同一指纹，从而可以安全重放首次结果。
func ImportRequestHash(operationType, contentHash, resourceType string, selectedRows []int) string {
	rows := append([]int(nil), selectedRows...)
	sort.Ints(rows)
	unique := rows[:0]
	for i, row := range rows {
		if i > 0 && rows[i-1] == row {
			continue
		}
		unique = append(unique, row)
	}
	parts := []string{
		strings.TrimSpace(operationType),
		strings.ToLower(strings.TrimSpace(contentHash)),
		strings.ToUpper(strings.TrimSpace(resourceType)),
		strings.Join(intStrings(unique), ","),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func intStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Itoa(value))
	}
	return out
}

// runImportConfirmation 是三个确认入口共用的幂等外壳：
// 交给仓储在单事务内执行写入并登记；命中已登记记录时比对请求指纹并回放首次响应。
func (s *Service) runImportConfirmation(ctx context.Context, op ImportOperation, write func(ImportRowWriter) (ImportConfirmationResult, error)) (ImportConfirmationResult, error) {
	store, ok := s.Repo.(ImportConfirmationStore)
	if !ok {
		return ImportConfirmationResult{}, errors.New("import confirmation store unavailable")
	}
	if op.ID == "" {
		op.ID = ulid.Make().String()
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now().UTC()
	}
	record, replayed, err := store.RunImportConfirmation(ctx, op, write)
	if err != nil {
		return ImportConfirmationResult{}, err
	}
	if replayed && record.RequestHash != op.RequestHash {
		// 同一个键被用于另一份文件或另一组行：不能覆盖首次结果，也不能重复写入。
		return ImportConfirmationResult{}, IdempotencyConflictError("该 Idempotency-Key 已用于另一份导入内容，请重新检测后再确认")
	}
	result := ImportConfirmationResult{}
	if strings.TrimSpace(record.ResponseJSON) != "" {
		if err := json.Unmarshal([]byte(record.ResponseJSON), &result); err != nil {
			return ImportConfirmationResult{}, fmt.Errorf("import confirmation response unavailable: %w", err)
		}
	}
	return result, nil
}

// importSelection 把用户选择的文件行号映射到预检行。
// 选中行必须出现在预检结果中且不重复；未选择的行不写入，记为 SKIPPED。
func importSelection(available []int, selectedRows []int) (map[int]bool, error) {
	if len(selectedRows) == 0 {
		return nil, ValidationError("请选择有效的导入行")
	}
	known := make(map[int]bool, len(available))
	for _, row := range available {
		known[row] = true
	}
	selected := make(map[int]bool, len(selectedRows))
	for _, row := range selectedRows {
		if !known[row] {
			return nil, ValidationError("选择行号超出文件范围，请重新检测")
		}
		if selected[row] {
			return nil, ValidationError("选择行号重复，请重新检测")
		}
		selected[row] = true
	}
	return selected, nil
}

func capabilityImportRowNumbers(rows []CapabilityImportPreviewRow) []int {
	numbers := make([]int, 0, len(rows))
	for _, row := range rows {
		numbers = append(numbers, row.RowNo)
	}
	return numbers
}

func detectionCategoryImportRowNumbers(rows []DetectionCategoryImportPreviewRow) []int {
	numbers := make([]int, 0, len(rows))
	for _, row := range rows {
		numbers = append(numbers, row.RowNo)
	}
	return numbers
}

// importRowFailure 把单行失败转换为带文件行号的可展示错误。
// 只有校验类失败（编号占用、编码停用等）是 422；基础设施故障保留原错误，由接口层按 500/503 处理。
func importRowFailure(rowNo int, err error) error {
	if errors.Is(err, ErrValidation) {
		reason := strings.TrimSpace(UserMessage(err))
		if reason == "" {
			reason = "校验未通过，请刷新后重新检测"
		}
		return ValidationError(fmt.Sprintf("第 %d 行：%s", rowNo, reason))
	}
	return fmt.Errorf("导入第 %d 行失败: %w", rowNo, err)
}

func importRowRejection(rowNo int, rowErrors []string) error {
	reason := strings.Join(rowErrors, "；")
	if strings.TrimSpace(reason) == "" {
		reason = "校验未通过，请刷新后重新检测"
	}
	return ValidationError(fmt.Sprintf("第 %d 行：%s", rowNo, reason))
}

// ConfirmCapabilityImport 确认导入人员资质。
// 选中的全部行在单个事务内写入；任一行校验或保存失败整批回滚并返回带行号的 422。
func (s *Service) ConfirmCapabilityImport(ctx context.Context, p platform.Principal, idempotencyKey, requestHash string, rows []CapabilityImportPreviewRow, selectedRows []int) (ImportConfirmationResult, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return ImportConfirmationResult{}, err
	}
	key, err := NormalizeImportIdempotencyKey(idempotencyKey)
	if err != nil {
		return ImportConfirmationResult{}, err
	}
	op := ImportOperation{TenantID: p.TenantID, IdempotencyKey: key, OperationType: ImportOperationCapability, RequestHash: requestHash}
	return s.runImportConfirmation(ctx, op, func(writer ImportRowWriter) (ImportConfirmationResult, error) {
		selected, err := importSelection(capabilityImportRowNumbers(rows), selectedRows)
		if err != nil {
			return ImportConfirmationResult{}, err
		}
		result := ImportConfirmationResult{Errors: []string{}, Rows: []ImportOutcomeRow{}}
		for _, row := range rows {
			if !selected[row.RowNo] {
				result.Rows = append(result.Rows, ImportOutcomeRow{RowNo: row.RowNo, Status: "SKIPPED", Message: "未选择"})
				continue
			}
			if row.Status != "READY" || (row.Action != "CREATE" && row.Action != "UPDATE") {
				return ImportConfirmationResult{}, importRowRejection(row.RowNo, row.Errors)
			}
			item := row.Capability
			// 确认端不信任行负载：人员资质入口只接受 PERSON，设备与非人员行整批拒绝。
			if item.ResourceType != "PERSON" {
				return ImportConfirmationResult{}, ValidationError(fmt.Sprintf("第 %d 行：只能导入人员资质，设备请使用设备能力管理", row.RowNo))
			}
			if err := validateCapability(item); err != nil {
				return ImportConfirmationResult{}, importRowFailure(row.RowNo, err)
			}
			item.TenantID = p.TenantID
			item.ResourceType = "PERSON"
			if _, saveErr := writer.UpsertCapability(ctx, item, p.UserID); saveErr != nil {
				return ImportConfirmationResult{}, importRowFailure(row.RowNo, saveErr)
			}
			result.Imported++
			result.Rows = append(result.Rows, ImportOutcomeRow{RowNo: row.RowNo, Status: "IMPORTED", Message: "导入成功"})
		}
		return result, nil
	})
}

// ConfirmEquipmentImport 确认导入设备能力。
// CREATE 行必须走纯插入（禁止冲突覆盖），UPDATE 行走既有设备覆盖；两者与台账同事务。
func (s *Service) ConfirmEquipmentImport(ctx context.Context, p platform.Principal, idempotencyKey, requestHash string, rows []CapabilityImportPreviewRow, selectedRows []int) (ImportConfirmationResult, error) {
	if err := requireApplicationAuthorization(p, "project.device.manage"); err != nil {
		return ImportConfirmationResult{}, err
	}
	key, err := NormalizeImportIdempotencyKey(idempotencyKey)
	if err != nil {
		return ImportConfirmationResult{}, err
	}
	op := ImportOperation{TenantID: p.TenantID, IdempotencyKey: key, OperationType: ImportOperationEquipment, RequestHash: requestHash}
	return s.runImportConfirmation(ctx, op, func(writer ImportRowWriter) (ImportConfirmationResult, error) {
		selected, err := importSelection(capabilityImportRowNumbers(rows), selectedRows)
		if err != nil {
			return ImportConfirmationResult{}, err
		}
		result := ImportConfirmationResult{Errors: []string{}, Rows: []ImportOutcomeRow{}}
		for _, row := range rows {
			if !selected[row.RowNo] {
				result.Rows = append(result.Rows, ImportOutcomeRow{RowNo: row.RowNo, Status: "SKIPPED", Message: "未选择"})
				continue
			}
			if row.Status != "READY" || (row.Action != "CREATE" && row.Action != "UPDATE") {
				return ImportConfirmationResult{}, importRowRejection(row.RowNo, row.Errors)
			}
			item := row.Capability
			// 设备入口只接受 EQUIPMENT：人员资质行不得借设备确认写入。
			if item.ResourceType != "EQUIPMENT" {
				return ImportConfirmationResult{}, ValidationError(fmt.Sprintf("第 %d 行：设备能力导入只接受 EQUIPMENT", row.RowNo))
			}
			if err := validateCapability(item); err != nil {
				return ImportConfirmationResult{}, importRowFailure(row.RowNo, err)
			}
			item.TenantID = p.TenantID
			item.ResourceType = "EQUIPMENT"
			var saveErr error
			if row.Action == "CREATE" {
				_, saveErr = writer.CreateEquipmentForImport(ctx, item, p.UserID)
			} else {
				_, saveErr = writer.UpsertCapability(ctx, item, p.UserID)
			}
			if saveErr != nil {
				return ImportConfirmationResult{}, importRowFailure(row.RowNo, saveErr)
			}
			result.Imported++
			result.Rows = append(result.Rows, ImportOutcomeRow{RowNo: row.RowNo, Status: "IMPORTED", Message: "导入成功"})
		}
		return result, nil
	})
}

// ConfirmDetectionCategoryImport 确认导入检测类别。
// 选中行全部保存成功才提交；任一行失败整批回滚，不会留下只更新了一半的类别目录。
func (s *Service) ConfirmDetectionCategoryImport(ctx context.Context, p platform.Principal, idempotencyKey, requestHash string, rows []DetectionCategoryImportPreviewRow, selectedRows []int) (ImportConfirmationResult, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return ImportConfirmationResult{}, err
	}
	key, err := NormalizeImportIdempotencyKey(idempotencyKey)
	if err != nil {
		return ImportConfirmationResult{}, err
	}
	op := ImportOperation{TenantID: p.TenantID, IdempotencyKey: key, OperationType: ImportOperationDetectionCategory, RequestHash: requestHash}
	return s.runImportConfirmation(ctx, op, func(writer ImportRowWriter) (ImportConfirmationResult, error) {
		selected, err := importSelection(detectionCategoryImportRowNumbers(rows), selectedRows)
		if err != nil {
			return ImportConfirmationResult{}, err
		}
		result := ImportConfirmationResult{Errors: []string{}, Rows: []ImportOutcomeRow{}}
		for _, row := range rows {
			if !selected[row.RowNo] {
				result.Rows = append(result.Rows, ImportOutcomeRow{RowNo: row.RowNo, Status: "SKIPPED", Message: "未选择"})
				continue
			}
			if row.Status != "READY" {
				return ImportConfirmationResult{}, importRowRejection(row.RowNo, row.Errors)
			}
			if err := domain.ValidateDetectionCategory(row.Item); err != nil {
				return ImportConfirmationResult{}, importRowFailure(row.RowNo, ValidationError(err.Error()))
			}
			if _, saveErr := writer.SaveDetectionCategory(ctx, p.TenantID, row.Item, p.UserID); saveErr != nil {
				return ImportConfirmationResult{}, importRowFailure(row.RowNo, saveErr)
			}
			result.Imported++
			result.Rows = append(result.Rows, ImportOutcomeRow{RowNo: row.RowNo, Status: "IMPORTED", Message: "导入成功"})
		}
		return result, nil
	})
}

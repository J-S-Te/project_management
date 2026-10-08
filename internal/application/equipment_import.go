package application

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type EquipmentImportInput struct {
	RowNo      int               `json:"row_no"`
	Capability domain.Capability `json:"capability"`
	Errors     []string          `json:"errors"`
}

// CreateEquipmentForImport must perform a plain insert, never conflict-update.
type EquipmentImportCreator interface {
	CreateEquipmentForImport(context.Context, domain.Capability, string) (domain.Capability, error)
}

func (s *Service) ConfirmEquipmentImportRow(ctx context.Context, p platform.Principal, row CapabilityImportPreviewRow) (domain.Capability, error) {
	if row.Status != "READY" || (row.Action != "CREATE" && row.Action != "UPDATE") {
		return domain.Capability{}, ErrValidation
	}
	return s.upsertEquipment(ctx, p, row.Capability, row.Action == "CREATE")
}

// PreviewEquipmentImport validates the complete file without changing equipment.
func (s *Service) PreviewEquipmentImport(ctx context.Context, p platform.Principal, inputs []EquipmentImportInput) (CapabilityImportPreview, error) {
	result := CapabilityImportPreview{Rows: []CapabilityImportPreviewRow{}}
	if err := requireApplicationAuthorization(p, "project.device.manage"); err != nil {
		return result, err
	}
	if len(inputs) == 0 || len(inputs) > 500 {
		return result, ValidationError("每次导入需要 1 至 500 条设备能力")
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return result, err
	}
	known, err := repo.ListCapabilities(ctx, p.TenantID, "EQUIPMENT")
	if err != nil {
		return result, err
	}
	rules, err := s.loadCapabilityCodeRules(ctx, p.TenantID)
	if err != nil {
		return result, err
	}
	// Reserve explicit numbers before allocating blank numbers, including later rows.
	allocated := append([]domain.Capability(nil), known...)
	for _, input := range inputs {
		if id := strings.TrimSpace(input.Capability.ResourceID); id != "" {
			allocated = append(allocated, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: id})
		}
	}
	seen := map[string][]int{}
	for _, input := range inputs {
		item := input.Capability
		normalizeCapability(&item)
		item.Status = strings.ToUpper(strings.TrimSpace(item.Status))
		if item.Status == "INACTIVE" {
			item.Status = "DISABLED"
		}
		if item.Status == "" {
			item.Status = "ACTIVE"
		}
		row := CapabilityImportPreviewRow{Action: "CREATE", RowNo: input.RowNo, ResourceName: item.ResourceName, Status: "READY", Errors: append([]string{}, input.Errors...)}
		if item.ResourceType != "EQUIPMENT" {
			row.Errors = append(row.Errors, "设备能力导入只接受 EQUIPMENT")
		}
		if item.ResourceName == "" || utf8.RuneCountInString(item.ResourceName) > 128 || strings.IndexFunc(item.ResourceName, unicode.IsControl) >= 0 {
			row.Errors = append(row.Errors, "设备名称必填且不能超过 128 个字符或包含控制字符")
		}
		if len(item.Codes) == 0 || len(item.Codes) > 50 {
			row.Errors = append(row.Errors, "请填写 1 至 50 个设备能力编码")
		}
		if item.Status != "ACTIVE" && item.Status != "DISABLED" {
			row.Errors = append(row.Errors, "状态只能填写有效 / 停用或 ACTIVE / DISABLED")
		}
		if utf8.RuneCountInString(item.ResourceID) > 64 || strings.IndexFunc(item.ResourceID, unicode.IsControl) >= 0 || (item.ResourceID != "" && strings.ContainsRune("=+-@", rune(item.ResourceID[0]))) {
			row.Errors = append(row.Errors, "设备编号格式无效")
		}
		if !item.ValidFrom.IsZero() && !item.ValidUntil.IsZero() && item.ValidFrom.After(item.ValidUntil) {
			row.Errors = append(row.Errors, "检定开始日期不能晚于检定截止日期")
		}
		for _, existing := range known {
			if existing.ResourceID == item.ResourceID && item.ResourceID != "" {
				row.Action = "UPDATE"
			}
		}
		if strings.TrimSpace(item.UsageScope) == "" {
			item.UsageScope = existingUsageScope(known, item.ResourceID)
		}
		item.UsageScope = strings.ToUpper(strings.TrimSpace(firstNonEmpty(item.UsageScope, domain.EquipmentUsageAny)))
		if item.UsageScope != domain.EquipmentUsageAny && item.UsageScope != domain.EquipmentUsageCompanyOnly {
			row.Errors = append(row.Errors, "使用范围只能填写可借出 / 仅在公司使用或 ANY / COMPANY_ONLY")
		}
		if codeErr := validateCapabilityCodesAgainstCatalog(rules, "EQUIPMENT", item.Codes, existingCapabilityCodes(known, "EQUIPMENT", item.ResourceID)); codeErr != nil {
			row.Errors = append(row.Errors, codeErr.Error())
		}
		if item.ResourceID == "" {
			assignResourceID(allocated, &item)
			allocated = append(allocated, item)
		}
		seen[item.ResourceID] = append(seen[item.ResourceID], len(result.Rows))
		row.Capability = item
		result.Rows = append(result.Rows, row)
	}
	for _, indexes := range seen {
		if len(indexes) > 1 {
			for _, index := range indexes {
				result.Rows[index].Errors = append(result.Rows[index].Errors, "同一文件设备编号重复，请合并为一行")
			}
		}
	}
	result.Total = len(result.Rows)
	for i := range result.Rows {
		if len(result.Rows[i].Errors) > 0 {
			result.Rows[i].Status = "INVALID"
			result.Invalid++
		} else {
			result.Valid++
		}
	}
	return result, nil
}

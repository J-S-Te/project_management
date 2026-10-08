package application

import (
	"context"
	"strings"
	"unicode"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type DetectionCategoryImportInput struct {
	RowNo  int
	Item   domain.DetectionCategory
	Errors []string
}
type DetectionCategoryImportPreviewRow struct {
	RowNo  int                      `json:"row_no"`
	Action string                   `json:"action"`
	Status string                   `json:"status"`
	Errors []string                 `json:"errors"`
	Item   domain.DetectionCategory `json:"item"`
}
type DetectionCategoryImportPreview struct {
	Total   int                                 `json:"total"`
	Valid   int                                 `json:"valid"`
	Invalid int                                 `json:"invalid"`
	Rows    []DetectionCategoryImportPreviewRow `json:"rows"`
}

// PreviewDetectionCategoryImport deliberately shares the write-side code catalog policy.
// It reads configuration only and never creates categories or rules.
func (s *Service) PreviewDetectionCategoryImport(ctx context.Context, p platform.Principal, inputs []DetectionCategoryImportInput) (DetectionCategoryImportPreview, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return DetectionCategoryImportPreview{}, err
	}
	if len(inputs) == 0 || len(inputs) > 500 {
		return DetectionCategoryImportPreview{}, ValidationError("需要 1 至 500 条检测类别")
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return DetectionCategoryImportPreview{}, err
	}
	existing, err := repo.ListDetectionCategories(ctx, p.TenantID)
	if err != nil {
		return DetectionCategoryImportPreview{}, err
	}
	rules, err := s.loadCapabilityCodeRules(ctx, p.TenantID)
	if err != nil {
		return DetectionCategoryImportPreview{}, err
	}
	known := map[string]bool{}
	for _, item := range existing {
		known[strings.ToLower(item.Category)] = true
	}
	counts := map[string]int{}
	for _, input := range inputs {
		counts[strings.ToLower(strings.TrimSpace(input.Item.Category))]++
	}
	result := DetectionCategoryImportPreview{Total: len(inputs), Rows: []DetectionCategoryImportPreviewRow{}}
	for _, input := range inputs {
		item := input.Item
		item.Category = strings.TrimSpace(item.Category)
		item.SystemStandard = strings.TrimSpace(item.SystemStandard)
		item.RequiredQualifications = strings.TrimSpace(item.RequiredQualifications)
		item.RequiredCodes = strings.Join(normalizeCapabilityCodes(domain.SplitCapabilityCodes(item.RequiredCodes)), ",")
		item.SpecialMethod = strings.ToUpper(strings.TrimSpace(item.SpecialMethod))
		row := DetectionCategoryImportPreviewRow{RowNo: input.RowNo, Action: "CREATE", Status: "READY", Errors: append([]string{}, input.Errors...), Item: item}
		if known[strings.ToLower(item.Category)] {
			row.Action = "UPDATE"
		}
		if err := domain.ValidateDetectionCategory(item); err != nil {
			row.Errors = append(row.Errors, err.Error())
		}
		if item.Category == "示例检测类别（请替换）" {
			row.Errors = append(row.Errors, "请将示例检测类别替换为真实类别")
		}
		for _, field := range []struct {
			label, value string
			limit        int
		}{{"默认体系要求", item.SystemStandard, 128}, {"必备资质", item.RequiredQualifications, 255}, {"必检能力码", item.RequiredCodes, 255}, {"检测类别", item.Category, 128}} {
			if len([]rune(field.value)) > field.limit {
				row.Errors = append(row.Errors, field.label+"过长")
			}
			if strings.ContainsFunc(field.value, unicode.IsControl) {
				row.Errors = append(row.Errors, field.label+"不能包含控制字符")
			}
		}
		if strings.HasPrefix(item.Category, "=") || strings.HasPrefix(item.Category, "+") || strings.HasPrefix(item.Category, "-") || strings.HasPrefix(item.Category, "@") {
			row.Errors = append(row.Errors, "检测类别不能以公式字符开头")
		}
		if counts[strings.ToLower(item.Category)] > 1 {
			row.Errors = append(row.Errors, "文件内检测类别重复，请保留一条")
		}
		if err := validateRequiredCodesAgainstCatalog(rules, domain.SplitCapabilityCodes(item.RequiredCodes)); err != nil {
			row.Errors = append(row.Errors, err.Error())
		}
		if len(row.Errors) > 0 {
			row.Status = "INVALID"
			result.Invalid++
		} else {
			result.Valid++
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

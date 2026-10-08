package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type CapabilityImportInput struct {
	RowNo            int               `json:"row_no"`
	Capability       domain.Capability `json:"capability"`
	OrganizationName string            `json:"organization_name"`
}
type CapabilityImportPreviewRow struct {
	Action           string            `json:"action"`
	RowNo            int               `json:"row_no"`
	ResourceName     string            `json:"resource_name"`
	OrganizationName string            `json:"organization_name"`
	Status           string            `json:"status"`
	Errors           []string          `json:"errors"`
	Capability       domain.Capability `json:"capability"`
}
type CapabilityImportPreview struct {
	Total   int                          `json:"total"`
	Valid   int                          `json:"valid"`
	Invalid int                          `json:"invalid"`
	Rows    []CapabilityImportPreviewRow `json:"rows"`
}

// PreviewCapabilityImport never writes. Confirmation repeats directory/catalog validation.
func (s *Service) PreviewCapabilityImport(ctx context.Context, p platform.Principal, inputs []CapabilityImportInput) (CapabilityImportPreview, error) {
	result := CapabilityImportPreview{Rows: []CapabilityImportPreviewRow{}}
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return result, err
	}
	if len(inputs) == 0 || len(inputs) > 500 {
		return result, ValidationError("每次导入需要 1 至 500 条人员资质")
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return result, err
	}
	known, err := repo.ListCapabilities(ctx, p.TenantID, "PERSON")
	if err != nil {
		return result, err
	}
	rules, err := s.loadCapabilityCodeRules(ctx, p.TenantID)
	if err != nil {
		return result, err
	}
	seen := map[string][]int{}
	type lookupResult struct {
		person platform.OwnerDirectoryUser
		err    error
	}
	lookups := map[string]lookupResult{}
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
		row := CapabilityImportPreviewRow{Action: "CREATE", RowNo: input.RowNo, ResourceName: item.ResourceName, OrganizationName: strings.TrimSpace(input.OrganizationName), Status: "READY", Errors: []string{}, Capability: item}
		if item.ResourceType != "PERSON" {
			row.Errors = append(row.Errors, "只能导入人员资质，设备请使用设备能力管理")
		}
		if item.ResourceName == "" || utf8.RuneCountInString(item.ResourceName) > 255 || strings.IndexFunc(item.ResourceName, unicode.IsControl) >= 0 {
			row.Errors = append(row.Errors, "人员姓名必填且不能超过 255 个字符或包含控制字符")
		}
		if utf8.RuneCountInString(row.OrganizationName) > 255 || strings.IndexFunc(row.OrganizationName, unicode.IsControl) >= 0 {
			row.Errors = append(row.Errors, "组织名称格式无效")
		}
		if len(item.Codes) == 0 || len(item.Codes) > 50 {
			row.Errors = append(row.Errors, "请填写 1 至 50 个已启用的人员资质编码")
		}
		if item.Status != "ACTIVE" && item.Status != "DISABLED" {
			row.Errors = append(row.Errors, "状态只能填写有效 / 停用或 ACTIVE / DISABLED")
		}
		if utf8.RuneCountInString(item.ResourceID) > 128 || strings.IndexFunc(item.ResourceID, unicode.IsControl) >= 0 || (item.ResourceID != "" && strings.ContainsRune("=+-@", rune(item.ResourceID[0]))) {
			row.Errors = append(row.Errors, "资质编号格式无效")
		}
		if len(row.Errors) == 0 {
			lookupKey := item.ResourceName + "\x00" + row.OrganizationName
			lookup, cached := lookups[lookupKey]
			if !cached {
				lookup.person, lookup.err = s.resolveImportPerson(ctx, item.ResourceName, row.OrganizationName)
				lookups[lookupKey] = lookup
			}
			person, lookupErr := lookup.person, lookup.err
			if lookupErr != nil {
				row.Errors = append(row.Errors, lookupErr.Error())
			} else {
				item.UserID, item.ResourceName = person.UserID, person.DisplayName
				row.ResourceName = person.DisplayName
				for _, org := range person.Organizations {
					if row.OrganizationName == "" && (org.IsPrimary || len(person.Organizations) == 1) {
						row.OrganizationName = org.OrganizationName
					}
				}
				for _, existing := range known {
					if existing.UserID == item.UserID && item.ResourceID != "" && existing.ResourceID != item.ResourceID {
						row.Errors = append(row.Errors, "该人员已有其他资质档案，请使用原资质编号维护")
					}
					if existing.UserID == item.UserID && item.ResourceID == "" {
						item.ResourceID = existing.ResourceID
						row.Action = "UPDATE"
					}
					if item.ResourceID != "" && existing.ResourceID == item.ResourceID {
						row.Action = "UPDATE"
						if existing.UserID != item.UserID {
							row.Errors = append(row.Errors, "资质编号已属于其他人员，不能覆盖")
						}
					}
				}
				if codeErr := validateCapabilityCodesAgainstCatalog(rules, "PERSON", item.Codes, existingCapabilityCodes(known, "PERSON", item.ResourceID)); codeErr != nil {
					row.Errors = append(row.Errors, codeErr.Error())
				}
				if item.ResourceID == "" {
					assignResourceID(known, &item)
				}
				if row.Action == "CREATE" && len(row.Errors) == 0 {
					known = append(known, item)
				}
				seen["resource:"+item.ResourceID] = append(seen["resource:"+item.ResourceID], len(result.Rows))
				seen[item.UserID] = append(seen[item.UserID], len(result.Rows))
			}
		}
		row.Capability = item
		result.Rows = append(result.Rows, row)
	}
	for _, indexes := range seen {
		if len(indexes) > 1 {
			for _, index := range indexes {
				result.Rows[index].Errors = append(result.Rows[index].Errors, "同一文件中人员重复，请合并资质编码为一行")
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

func (s *Service) resolveImportPerson(ctx context.Context, name, organization string) (platform.OwnerDirectoryUser, error) {
	if s.Personnel == nil {
		return platform.OwnerDirectoryUser{}, ValidationError("人员目录暂不可用，请稍后重新预检")
	}
	matches := map[string]platform.OwnerDirectoryUser{}
	seenUsers := map[string]bool{}
	total := int64(-1)
	for page := 1; page <= 20; page++ {
		result, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{Keyword: name, Page: page, PageSize: 50})
		if err != nil {
			return platform.OwnerDirectoryUser{}, ValidationError("人员目录查询失败，请稍后重新预检")
		}
		if total == -1 {
			total = result.Total
		}
		expected := total - int64((page-1)*50)
		if expected > 50 {
			expected = 50
		}
		if total < 0 || total > 1000 || result.Total != total || expected < 0 || int64(len(result.Items)) != expected {
			return platform.OwnerDirectoryUser{}, ValidationError("人员目录分页不完整或已变化，请重新预检")
		}
		for _, person := range result.Items {
			id := strings.TrimSpace(person.UserID)
			if id == "" || seenUsers[id] {
				return platform.OwnerDirectoryUser{}, ValidationError("人员目录返回重复或无效人员，请重新预检")
			}
			seenUsers[id] = true
			if strings.TrimSpace(person.DisplayName) != name || strings.TrimSpace(person.UserID) == "" {
				continue
			}
			allowed := organization == ""
			for _, org := range person.Organizations {
				if strings.TrimSpace(org.OrganizationName) == organization {
					allowed = true
				}
			}
			if allowed {
				matches[person.UserID] = person
			}
		}
		if result.Total <= int64(page*50) {
			if len(matches) != 1 {
				return platform.OwnerDirectoryUser{}, ValidationError("人员不存在、无权访问或姓名不唯一；请填写准确姓名和组织名称")
			}
			for _, person := range matches {
				return person, nil
			}
		}
		if len(result.Items) == 0 {
			return platform.OwnerDirectoryUser{}, ValidationError("人员目录分页异常，请稍后重新预检")
		}
	}
	return platform.OwnerDirectoryUser{}, ValidationError("同名人员过多，无法安全匹配，请联系管理员处理")
}

func capabilityImportSafeError(err error) string {
	if errors.Is(err, ErrValidation) {
		return err.Error()
	}
	if errors.Is(err, ErrPersonnelUnavailable) {
		return "人员目录暂不可用，请重新预检"
	}
	return fmt.Sprint("保存失败，请刷新后重试或联系管理员")
}

package httpapi

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/businessclock"
	"github.com/j-s-te/project-management/internal/domain"
)

const maximumCapabilityImportBytes = 2 << 20

var capabilityImportColumns = []string{"resource_type", "resource_id", "resource_name", "codes", "status", "valid_from", "valid_until"}

// capabilitiesImportResponseHeader 支持表头按名称定位列，不依赖固定列顺序。
func capabilityColumnIndexes(header []string) map[string]int {
	indexes := make(map[string]int, len(capabilityImportColumns))
	for i, name := range header {
		indexes[strings.ToLower(strings.TrimSpace(name))] = i
	}
	return indexes
}

func splitCapabilityCodes(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '，' || r == '；' || r == '|' || unicode.IsSpace(r)
	})
}

func parseFlexibleTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("时间格式 %q 不受支持", value)
}

func formatCapabilityTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
}

// capabilitiesFromImportCSV 把上传的记录解析为能力负载；无法解析的行返回错误信息。
// 首行被识别为表头时按列名定位字段，否则按固定列顺序读取。
func capabilitiesFromImportCSV(records [][]string) (rows []domain.Capability, errs []string) {
	if len(records) == 0 {
		return rows, errs
	}
	hasHeader := false
	for _, cell := range records[0] {
		normalized := strings.ToLower(strings.TrimSpace(cell))
		for _, column := range capabilityImportColumns {
			if normalized == column {
				hasHeader = true
				break
			}
		}
		if hasHeader {
			break
		}
	}
	indexes := map[string]int{}
	start := 0
	if hasHeader {
		indexes = capabilityColumnIndexes(records[0])
		start = 1
	}
	field := func(record []string, name string, fallback int) string {
		if hasHeader {
			if i, ok := indexes[name]; ok && i < len(record) {
				return strings.TrimSpace(record[i])
			}
			return ""
		}
		if fallback < len(record) {
			return strings.TrimSpace(record[fallback])
		}
		return ""
	}
	for i := start; i < len(records); i++ {
		line := fmt.Sprintf("csv 第 %d 行", i+1)
		record := records[i]
		resourceType := strings.ToUpper(field(record, "resource_type", 0))
		status := strings.ToUpper(field(record, "status", 4))
		if status == "" {
			status = "ACTIVE"
		}
		validFrom, validUntil := time.Time{}, time.Time{}
		if resourceType == "EQUIPMENT" {
			var err error
			validFrom, err = parseFlexibleTime(field(record, "valid_from", 5))
			if err != nil {
				errs = append(errs, line+": "+err.Error())
				continue
			}
			validUntil, err = parseFlexibleTime(field(record, "valid_until", 6))
			if err != nil {
				errs = append(errs, line+": "+err.Error())
				continue
			}
		}
		rows = append(rows, domain.Capability{
			ResourceType: resourceType,
			ResourceID:   field(record, "resource_id", 1),
			ResourceName: field(record, "resource_name", 2),
			Codes:        splitCapabilityCodes(field(record, "codes", 3)),
			Status:       status,
			ValidFrom:    validFrom,
			ValidUntil:   validUntil,
		})
	}
	return rows, errs
}

func (h *Handler) importCapabilities(c *gin.Context) {
	h.confirmPersonnelImport(c)
}

func (h *Handler) importEquipment(c *gin.Context) {
	h.confirmEquipmentImport(c)
}

// escapeCSVFormulaCell 对公式注入载荷加前导单引号（OWASP CSV Injection 防御）：
// 以 =、+、-、@ 开头，或前导含 tab/CR（表格软件会先剥掉这些控制字符再判定公式）的
// 单元格一律加 ' 前缀，使其按字面文本渲染；普通值原样返回，不改变既有导出内容。
// 口径对齐平台 SEC-B6 escapeCSVFormulaCell、Settlement safeCell 与 CRM safeCSV：
// 既有口径不含"纯数字豁免"，负数字面值同样按 '-' 公式前缀处理。
func escapeCSVFormulaCell(value string) string {
	if value == "" {
		return value
	}
	// 前导 tab/CR 本身就是公式解析绕过面，按同样方式转义。
	if strings.TrimLeft(value, "\t\r") != value {
		return "'" + value
	}
	switch value[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

// capabilityExportRow 把能力记录转换为导出 CSV 行，列顺序与 capabilityImportColumns 一致。
// 资源 ID、名称、编码等字段来自用户导入，可能以公式前缀或控制字符开头，
// 审计/业务人员在 Excel/WPS 打开导出文件时会被解释为公式，因此逐格转义。
func capabilityExportRow(item domain.Capability) []string {
	validFrom, validUntil := "", ""
	if item.ResourceType == "EQUIPMENT" {
		validFrom = formatCapabilityTime(item.ValidFrom)
		validUntil = formatCapabilityTime(item.ValidUntil)
	}
	return []string{
		escapeCSVFormulaCell(item.ResourceType),
		escapeCSVFormulaCell(item.ResourceID),
		escapeCSVFormulaCell(item.ResourceName),
		escapeCSVFormulaCell(strings.Join(item.Codes, ";")),
		escapeCSVFormulaCell(item.Status),
		escapeCSVFormulaCell(validFrom),
		escapeCSVFormulaCell(validUntil),
	}
}

func (h *Handler) exportCapabilities(c *gin.Context) {
	typ := strings.ToUpper(strings.TrimSpace(c.Query("resource_type")))
	if typ != "" && typ != "PERSON" && typ != "EQUIPMENT" {
		writeServiceError(c, application.ErrValidation)
		return
	}
	items, err := h.service.ListCapabilities(c.Request.Context(), principal(c), typ)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	buffer := &bytes.Buffer{}
	buffer.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(buffer)
	_ = writer.Write(capabilityImportColumns)
	for _, item := range items {
		_ = writer.Write(capabilityExportRow(item))
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	// 导出文件名里的日期段时间按北京业务时间：凌晨导出不能标成前一天的日期。
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=capabilities-%s.csv", businessclock.Now().Format("20060102-150405")))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

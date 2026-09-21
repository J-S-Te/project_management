package httpapi

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/j-s-te/project-management/internal/application"
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
	file, err := c.FormFile("file")
	if err != nil || file.Size > maximumCapabilityImportBytes {
		writeServiceError(c, application.ErrValidation)
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	defer opened.Close()
	content, err := io.ReadAll(io.LimitReader(opened, maximumCapabilityImportBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumCapabilityImportBytes {
		writeServiceError(c, application.ErrValidation)
		return
	}
	if h.service.EvidenceFiles == nil {
		writeError(c, http.StatusServiceUnavailable, "PM_FILE_GATEWAY_UNAVAILABLE", "统一文件网关暂不可用，导入未执行")
		return
	}
	if _, err = h.service.EvidenceFiles.UploadImport(c.Request.Context(), c.GetHeader("X-Request-ID"), "CAPABILITY", file.Filename, "text/csv", bytes.NewReader(content)); err != nil {
		writeError(c, http.StatusServiceUnavailable, "PM_FILE_GATEWAY_UNAVAILABLE", "文件校验暂不可用，导入未执行")
		return
	}
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	rows, parseErrors := capabilitiesFromImportCSV(records)
	result, err := h.service.ImportCapabilities(c.Request.Context(), principal(c), rows)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	result.Errors = append(parseErrors, result.Errors...)
	result.Skipped += len(parseErrors)
	writeData(c, http.StatusOK, result)
}

func (h *Handler) importEquipment(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil || file.Size > maximumCapabilityImportBytes {
		writeServiceError(c, application.ErrValidation)
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	defer opened.Close()
	content, err := io.ReadAll(io.LimitReader(opened, maximumCapabilityImportBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumCapabilityImportBytes {
		writeServiceError(c, application.ErrValidation)
		return
	}
	if h.service.EvidenceFiles == nil {
		writeError(c, http.StatusServiceUnavailable, "PM_FILE_GATEWAY_UNAVAILABLE", "统一文件网关暂不可用，导入未执行")
		return
	}
	if _, err = h.service.EvidenceFiles.UploadImport(c.Request.Context(), c.GetHeader("X-Request-ID"), "CAPABILITY", file.Filename, "text/csv", bytes.NewReader(content)); err != nil {
		writeError(c, http.StatusServiceUnavailable, "PM_FILE_GATEWAY_UNAVAILABLE", "文件校验暂不可用，导入未执行")
		return
	}
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	rows, parseErrors := capabilitiesFromImportCSV(records)
	imported, skipped := 0, len(parseErrors)
	errorsFound := append([]string(nil), parseErrors...)
	for index, row := range rows {
		if strings.ToUpper(strings.TrimSpace(row.ResourceType)) != "EQUIPMENT" {
			skipped++
			errorsFound = append(errorsFound, fmt.Sprintf("第 %d 行：设备导入仅接受 EQUIPMENT", index+2))
			continue
		}
		if _, saveErr := h.service.UpsertEquipment(c.Request.Context(), principal(c), row); saveErr != nil {
			skipped++
			errorsFound = append(errorsFound, fmt.Sprintf("第 %d 行：设备数据无效、能力编码不可用或设备正在被占用", index+2))
			continue
		}
		imported++
	}
	writeData(c, http.StatusOK, map[string]any{"imported": imported, "skipped": skipped, "errors": errorsFound})
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
		validFrom, validUntil := "", ""
		if item.ResourceType == "EQUIPMENT" {
			validFrom = formatCapabilityTime(item.ValidFrom)
			validUntil = formatCapabilityTime(item.ValidUntil)
		}
		_ = writer.Write([]string{
			item.ResourceType,
			item.ResourceID,
			item.ResourceName,
			strings.Join(item.Codes, ";"),
			item.Status,
			validFrom,
			validUntil,
		})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=capabilities-%s.csv", time.Now().UTC().Format("20060102-150405")))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

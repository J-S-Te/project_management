package httpapi

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

var equipmentImportHeaders = []string{"设备名称", "能力编码", "状态", "设备编号", "检定开始日期", "检定截止日期", "使用范围"}

func (h *Handler) equipmentImportTemplate(c *gin.Context) {
	buffer := bytes.NewBufferString("\xef\xbb\xbf")
	if err := csv.NewWriter(buffer).WriteAll([][]string{equipmentImportHeaders}); err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	c.Header("Content-Disposition", "attachment; filename=equipment-capability-template.csv")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

func (h *Handler) readEquipmentImport(c *gin.Context) ([]application.EquipmentImportInput, error) {
	file, err := c.FormFile("file")
	if err != nil || file.Size > maximumCapabilityImportBytes || !strings.EqualFold(filepath.Ext(file.Filename), ".csv") {
		return nil, application.ValidationError("请上传不超过 2MB 的 CSV 文件")
	}
	opened, err := file.Open()
	if err != nil {
		return nil, application.ErrValidation
	}
	defer opened.Close()
	content, err := io.ReadAll(io.LimitReader(opened, maximumCapabilityImportBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumCapabilityImportBytes || !utf8.Valid(content) {
		return nil, application.ValidationError("文件为空、超过 2MB 或不是 UTF-8 编码")
	}
	if h.service.EvidenceFiles == nil {
		return nil, errCapabilityImportGateway
	}
	if _, err := h.service.EvidenceFiles.UploadImport(c.Request.Context(), c.GetHeader("X-Request-ID"), "CAPABILITY", file.Filename, "text/csv", bytes.NewReader(content)); err != nil {
		return nil, errCapabilityImportGateway
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(content), "\ufeff")))
	reader.FieldsPerRecord = -1
	records := [][]string{}
	for {
		row, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, application.ValidationError("CSV 格式错误，请使用下载的模板")
		}
		if len(records) > 500 {
			return nil, application.ValidationError("最多支持 500 条设备能力")
		}
		for _, cell := range row {
			if len(cell) > 2000 {
				return nil, application.ValidationError("单元格内容过长")
			}
		}
		records = append(records, row)
	}
	return equipmentImportInputs(records)
}

func equipmentImportInputs(records [][]string) ([]application.EquipmentImportInput, error) {
	if len(records) < 2 || len(records) > 501 {
		return nil, application.ValidationError("模板需要表头和 1 至 500 条设备能力")
	}
	modern := len(records[0]) == len(equipmentImportHeaders)
	if modern {
		for i, header := range equipmentImportHeaders {
			if strings.TrimSpace(records[0][i]) != header {
				modern = false
				break
			}
		}
	}
	indexes := capabilityColumnIndexes(records[0])
	if !modern {
		seen := map[string]bool{}
		for _, header := range records[0] {
			key := strings.ToLower(strings.TrimSpace(header))
			if key == "" || seen[key] {
				return nil, application.ValidationError("表头为空或重复，请下载最新模板")
			}
			seen[key] = true
		}
		for _, header := range []string{"resource_type", "resource_id", "resource_name", "codes"} {
			if _, ok := indexes[header]; !ok {
				return nil, application.ValidationError("表头不匹配，请下载最新模板")
			}
		}
	}
	inputs := []application.EquipmentImportInput{}
	for i, record := range records[1:] {
		if len(record) != len(records[0]) {
			return nil, application.ValidationError(fmt.Sprintf("第 %d 行列数与表头不一致", i+2))
		}
		field := func(name string) string {
			if index, ok := indexes[name]; ok {
				return strings.TrimSpace(record[index])
			}
			return ""
		}
		item := domain.Capability{ResourceType: strings.ToUpper(field("resource_type")), ResourceName: field("resource_name"), ResourceID: field("resource_id"), Codes: splitCapabilityCodes(field("codes")), Status: field("status"), UsageScope: field("usage_scope")}
		from, until := field("valid_from"), field("valid_until")
		if modern {
			item = domain.Capability{ResourceType: "EQUIPMENT", ResourceName: strings.TrimSpace(record[0]), Codes: splitCapabilityCodes(record[1]), Status: strings.TrimSpace(record[2]), ResourceID: strings.TrimSpace(record[3]), UsageScope: strings.TrimSpace(record[6])}
			from, until = strings.TrimSpace(record[4]), strings.TrimSpace(record[5])
		}
		switch item.Status {
		case "有效", "启用":
			item.Status = "ACTIVE"
		case "无效", "停用":
			item.Status = "DISABLED"
		}
		switch item.UsageScope {
		case "可借出":
			item.UsageScope = domain.EquipmentUsageAny
		case "仅在公司使用":
			item.UsageScope = domain.EquipmentUsageCompanyOnly
		}
		input := application.EquipmentImportInput{RowNo: i + 2, Capability: item, Errors: []string{}}
		for _, date := range []struct {
			value, label string
			target       *time.Time
		}{{from, "检定开始日期", &input.Capability.ValidFrom}, {until, "检定截止日期", &input.Capability.ValidUntil}} {
			if date.value == "" {
				continue
			}
			var parsed time.Time
			var err error
			if modern {
				parsed, err = time.Parse("2006-01-02", date.value)
			} else {
				parsed, err = parseFlexibleTime(date.value)
			}
			if err != nil {
				input.Errors = append(input.Errors, date.label+"格式错误，请填写有效日期 YYYY-MM-DD")
			} else {
				*date.target = parsed
			}
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func (h *Handler) previewEquipmentImport(c *gin.Context) {
	inputs, err := h.readEquipmentImport(c)
	if err != nil {
		writeCapabilityImportError(c, err)
		return
	}
	result, err := h.service.PreviewEquipmentImport(c.Request.Context(), principal(c), inputs)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, result)
}

func (h *Handler) confirmEquipmentImport(c *gin.Context) {
	inputs, err := h.readEquipmentImport(c)
	if err != nil {
		writeCapabilityImportError(c, err)
		return
	}
	selected := map[int]bool{}
	if value := strings.TrimSpace(c.PostForm("selected_rows")); value != "" {
		var rows []int
		if json.Unmarshal([]byte(value), &rows) != nil || len(rows) == 0 || len(rows) > 500 {
			writeServiceError(c, application.ValidationError("请选择有效的导入行"))
			return
		}
		for _, row := range rows {
			if row < 2 || row > len(inputs)+1 || selected[row] {
				writeServiceError(c, application.ValidationError("选择行号重复或超出文件范围"))
				return
			}
			selected[row] = true
		}
	} else {
		for _, input := range inputs {
			selected[input.RowNo] = true
		}
	}
	preview, err := h.service.PreviewEquipmentImport(c.Request.Context(), principal(c), inputs)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	type outcome struct {
		RowNo   int    `json:"row_no"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	outcomes := []outcome{}
	imported, skipped := 0, 0
	messages := []string{}
	for _, row := range preview.Rows {
		if !selected[row.RowNo] {
			outcomes = append(outcomes, outcome{row.RowNo, "SKIPPED", "未选择"})
			continue
		}
		message := strings.Join(row.Errors, "；")
		if row.Status == "READY" {
			if _, saveErr := h.service.ConfirmEquipmentImportRow(c.Request.Context(), principal(c), row); saveErr == nil {
				imported++
				outcomes = append(outcomes, outcome{row.RowNo, "IMPORTED", "导入成功"})
				continue
			} else {
				message = "保存失败，请刷新设备目录后重新预检"
			}
		}
		skipped++
		messages = append(messages, fmt.Sprintf("第 %d 行：%s", row.RowNo, message))
		outcomes = append(outcomes, outcome{row.RowNo, "FAILED", message})
	}
	writeData(c, http.StatusOK, gin.H{"imported": imported, "skipped": skipped, "errors": messages, "rows": outcomes})
}

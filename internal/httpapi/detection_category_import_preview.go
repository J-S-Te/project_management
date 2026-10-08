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
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

var detectionCategoryImportHeaders = []string{"检测类别", "默认体系要求", "必备资质（默认）", "必检能力码", "是否特殊方法", "状态"}

func (h *Handler) detectionCategoryImportTemplate(c *gin.Context) {
	rows := [][]string{detectionCategoryImportHeaders}
	filename := "detection-category-template.csv"
	if c.Query("example") == "true" {
		item := domain.DetectionCategory{Category: "示例检测类别（请替换）", SpecialMethod: "NO", Enabled: true}
		status := "停用"
		if item.Enabled {
			status = "启用"
		}
		rows = append(rows, []string{item.Category, item.SystemStandard, item.RequiredQualifications, item.RequiredCodes, item.SpecialMethod, status})
		filename = "detection-category-example.csv"
	}
	buffer := bytes.NewBufferString("\xef\xbb\xbf")
	if err := csv.NewWriter(buffer).WriteAll(rows); err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

func detectionCategoryImportInputs(records [][]string) ([]application.DetectionCategoryImportInput, error) {
	if len(records) < 2 || len(records) > 501 {
		return nil, application.ValidationError("模板需要表头和 1 至 500 条检测类别")
	}
	if len(records[0]) != len(detectionCategoryImportHeaders) {
		return nil, application.ValidationError("表头不匹配，请下载最新模板")
	}
	for i, header := range detectionCategoryImportHeaders {
		if strings.TrimSpace(records[0][i]) != header {
			return nil, application.ValidationError("表头不匹配，请下载最新模板")
		}
	}
	inputs := []application.DetectionCategoryImportInput{}
	for i, record := range records[1:] {
		if len(record) != 6 {
			return nil, application.ValidationError(fmt.Sprintf("第 %d 行列数与表头不一致", i+2))
		}
		for j := range record {
			record[j] = strings.TrimSpace(record[j])
		}
		input := application.DetectionCategoryImportInput{RowNo: i + 2, Errors: []string{}, Item: domain.DetectionCategory{Category: record[0], SystemStandard: record[1], RequiredQualifications: record[2], RequiredCodes: record[3]}}
		special, ok := map[string]string{"": "NO", "否": "NO", "NO": "NO", "可标记": "MARKABLE", "MARKABLE": "MARKABLE", "必为特殊方法": "REQUIRED", "REQUIRED": "REQUIRED"}[strings.ToUpper(record[4])]
		if !ok {
			input.Errors = append(input.Errors, "是否特殊方法取值不合法")
		}
		input.Item.SpecialMethod = special
		switch strings.ToUpper(record[5]) {
		case "", "启用", "ACTIVE", "TRUE":
			input.Item.Enabled = true
		case "停用", "禁用", "DISABLED", "INACTIVE", "FALSE":
			input.Item.Enabled = false
		default:
			input.Errors = append(input.Errors, "状态仅支持启用或停用")
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func (h *Handler) readDetectionCategoryImport(c *gin.Context) ([]application.DetectionCategoryImportInput, error) {
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
	if _, err := h.service.EvidenceFiles.UploadImport(c.Request.Context(), c.GetHeader("X-Request-ID"), "DETECTION_CATEGORY", file.Filename, "text/csv", bytes.NewReader(content)); err != nil {
		return nil, errCapabilityImportGateway
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(content), "\ufeff")))
	reader.FieldsPerRecord = -1
	records := [][]string{}
	lineNumbers := []int{}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, application.ValidationError("CSV 格式错误，请使用下载的模板")
		}
		if len(records) > 500 {
			return nil, application.ValidationError("最多支持 500 条检测类别")
		}
		for _, cell := range row {
			if len(cell) > 2000 {
				return nil, application.ValidationError("单元格内容过长")
			}
		}
		records = append(records, row)
		line, _ := reader.FieldPos(0)
		lineNumbers = append(lineNumbers, line)
	}
	inputs, err := detectionCategoryImportInputs(records)
	if err != nil {
		return nil, err
	}
	for i := range inputs {
		inputs[i].RowNo = lineNumbers[i+1]
	}
	return inputs, nil
}

func (h *Handler) previewDetectionCategoryImport(c *gin.Context) {
	inputs, err := h.readDetectionCategoryImport(c)
	if err != nil {
		writeCapabilityImportError(c, err)
		return
	}
	result, err := h.service.PreviewDetectionCategoryImport(c.Request.Context(), principal(c), inputs)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, result)
}

func (h *Handler) confirmDetectionCategoryImport(c *gin.Context) {
	inputs, err := h.readDetectionCategoryImport(c)
	if err != nil {
		writeCapabilityImportError(c, err)
		return
	}
	selected := map[int]bool{}
	available := map[int]bool{}
	for _, input := range inputs {
		available[input.RowNo] = true
	}
	if raw := strings.TrimSpace(c.PostForm("selected_rows")); raw != "" {
		var rows []int
		if json.Unmarshal([]byte(raw), &rows) != nil || len(rows) == 0 || len(rows) > 500 {
			writeServiceError(c, application.ValidationError("请选择有效导入行"))
			return
		}
		for _, row := range rows {
			if !available[row] || selected[row] {
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
	preview, err := h.service.PreviewDetectionCategoryImport(c.Request.Context(), principal(c), inputs)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	type outcome struct {
		RowNo   int    `json:"row_no"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	rows := []outcome{}
	messages := []string{}
	imported, skipped := 0, 0
	for _, row := range preview.Rows {
		if !selected[row.RowNo] {
			rows = append(rows, outcome{row.RowNo, "SKIPPED", "未选择"})
			continue
		}
		message := strings.Join(row.Errors, "；")
		if row.Status == "READY" {
			if _, err := h.service.SaveDetectionCategory(c.Request.Context(), principal(c), row.Item); err == nil {
				imported++
				rows = append(rows, outcome{row.RowNo, "IMPORTED", "导入成功"})
				continue
			} else {
				message = "保存失败，请刷新目录后重新检测"
			}
		}
		skipped++
		messages = append(messages, fmt.Sprintf("第 %d 行：%s", row.RowNo, message))
		rows = append(rows, outcome{row.RowNo, "FAILED", message})
	}
	writeData(c, http.StatusOK, gin.H{"imported": imported, "skipped": skipped, "errors": messages, "rows": rows})
}

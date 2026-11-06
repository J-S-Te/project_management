package httpapi

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
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

var personnelImportHeaders = []string{"人员姓名", "组织名称", "资质编码", "状态", "资质编号"}
var errCapabilityImportGateway = errors.New("capability import file gateway unavailable")

func writeCapabilityImportError(c *gin.Context, err error) {
	if errors.Is(err, errCapabilityImportGateway) {
		writeError(c, http.StatusServiceUnavailable, "PM_FILE_GATEWAY_UNAVAILABLE", "统一文件网关或文件安全校验暂不可用，导入未执行")
		return
	}
	writeServiceError(c, err)
}

// writeImportIdempotencyKeyError 对缺失或非法的 Idempotency-Key 返回 400：
// 这是请求头格式问题，不是业务数据校验失败（422），应在读取文件前拒绝。
func writeImportIdempotencyKeyError(c *gin.Context, err error) {
	writeError(c, http.StatusBadRequest, "PM_INVALID_IDEMPOTENCY_KEY", serviceMessage(err, "缺少或非法的 Idempotency-Key 请求头"))
}

// parseImportSelection 解析 selected_rows 表单字段（CSV 物理行号 JSON 数组）。
// 缺省表示整份文件；显式选择必须非空、不重复且落在本次解析出的行号内。
func parseImportSelection(c *gin.Context, available []int) ([]int, error) {
	value := strings.TrimSpace(c.PostForm("selected_rows"))
	if value == "" {
		return append([]int(nil), available...), nil
	}
	var rows []int
	if json.Unmarshal([]byte(value), &rows) != nil || len(rows) == 0 || len(rows) > 500 {
		return nil, application.ValidationError("请选择有效的导入行")
	}
	known := make(map[int]bool, len(available))
	for _, row := range available {
		known[row] = true
	}
	selected := make(map[int]bool, len(rows))
	unique := make([]int, 0, len(rows))
	for _, row := range rows {
		if !known[row] {
			return nil, application.ValidationError("选择行号重复或超出文件范围")
		}
		if selected[row] {
			return nil, application.ValidationError("选择行号重复或超出文件范围")
		}
		selected[row] = true
		unique = append(unique, row)
	}
	return unique, nil
}

func importRowNumbers(rows []application.CapabilityImportInput) []int {
	numbers := make([]int, 0, len(rows))
	for _, row := range rows {
		numbers = append(numbers, row.RowNo)
	}
	return numbers
}

func (h *Handler) capabilityImportTemplate(c *gin.Context) {
	buffer := bytes.NewBufferString("\xef\xbb\xbf")
	writer := csv.NewWriter(buffer)
	if err := writer.WriteAll([][]string{personnelImportHeaders}); err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	c.Header("Content-Disposition", "attachment; filename=personnel-qualification-template.csv")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

func (h *Handler) readPersonnelImport(c *gin.Context) ([]application.CapabilityImportInput, string, error) {
	file, err := c.FormFile("file")
	if err != nil || file.Size > maximumCapabilityImportBytes || !strings.EqualFold(filepath.Ext(file.Filename), ".csv") {
		return nil, "", application.ValidationError("请上传不超过 2MB 的 CSV 文件")
	}
	opened, err := file.Open()
	if err != nil {
		return nil, "", application.ErrValidation
	}
	defer opened.Close()
	content, err := io.ReadAll(io.LimitReader(opened, maximumCapabilityImportBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumCapabilityImportBytes || !utf8.Valid(content) {
		return nil, "", application.ValidationError("文件为空、超过 2MB 或不是 UTF-8 编码")
	}
	// 内容指纹先于网关/解析计算：确认请求的幂等指纹必须只由文件字节决定。
	contentHash := application.ImportContentHash(content)
	if h.service.EvidenceFiles == nil {
		return nil, "", errCapabilityImportGateway
	}
	// 幂等键必须由文件内容决定，不能用每个请求都不同的 X-Request-ID，
	// 否则预览与确认（以及重复点击）会把同一份文件重复上传到统一文件网关。
	if _, err := h.service.EvidenceFiles.UploadImport(c.Request.Context(), importUploadRequestID("CAPABILITY", file.Filename, content), "CAPABILITY", file.Filename, "text/csv", bytes.NewReader(content)); err != nil {
		return nil, "", errCapabilityImportGateway
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
			return nil, "", application.ValidationError("CSV 格式错误，请使用下载的模板")
		}
		if len(records) > 500 {
			return nil, "", application.ValidationError("最多支持 500 条人员资质")
		}
		for _, cell := range row {
			if len(cell) > 2000 {
				return nil, "", application.ValidationError("单元格内容过长")
			}
		}
		records = append(records, row)
	}
	inputs, err := personnelImportInputs(records)
	if err != nil {
		return nil, "", err
	}
	return inputs, contentHash, nil
}

func personnelImportInputs(records [][]string) ([]application.CapabilityImportInput, error) {
	if len(records) < 2 || len(records) > 501 {
		return nil, application.ValidationError("模板需要表头和 1 至 500 条人员资质")
	}
	inputs := make([]application.CapabilityImportInput, 0, len(records)-1)
	modern := len(records[0]) == len(personnelImportHeaders)
	if modern {
		for i, header := range personnelImportHeaders {
			if strings.TrimSpace(records[0][i]) != header {
				modern = false
				break
			}
		}
	}
	if !modern {
		seenHeaders := map[string]bool{}
		for _, header := range records[0] {
			key := strings.ToLower(strings.TrimSpace(header))
			if key == "" || seenHeaders[key] {
				return nil, application.ValidationError("表头为空或重复，请下载最新导入模板")
			}
			seenHeaders[key] = true
		}
		indexes := capabilityColumnIndexes(records[0])
		for _, header := range []string{"resource_type", "resource_id", "resource_name", "codes"} {
			if _, ok := indexes[header]; !ok {
				return nil, application.ValidationError("表头不匹配，请下载最新导入模板")
			}
		}
	}
	for i, record := range records[1:] {
		rowNo := i + 2
		if modern {
			if len(record) != 5 {
				return nil, application.ValidationError(fmt.Sprintf("第 %d 行列数错误，应为 5 列", rowNo))
			}
			status := strings.TrimSpace(record[3])
			if status == "有效" || status == "启用" {
				status = "ACTIVE"
			}
			if status == "无效" || status == "停用" {
				status = "DISABLED"
			}
			inputs = append(inputs, application.CapabilityImportInput{RowNo: rowNo, OrganizationName: strings.TrimSpace(record[1]), Capability: domain.Capability{ResourceType: "PERSON", ResourceName: strings.TrimSpace(record[0]), Codes: splitCapabilityCodes(record[2]), Status: status, ResourceID: strings.TrimSpace(record[4])}})
		} else {
			if len(record) != len(records[0]) {
				return nil, application.ValidationError(fmt.Sprintf("第 %d 行列数与表头不一致", rowNo))
			}
			parsed, parseErrs := capabilitiesFromImportCSV([][]string{records[0], record})
			if len(parseErrs) != 0 || len(parsed) != 1 {
				return nil, application.ValidationError(fmt.Sprintf("第 %d 行格式错误", rowNo))
			}
			inputs = append(inputs, application.CapabilityImportInput{RowNo: rowNo, Capability: parsed[0]})
		}
	}
	return inputs, nil
}

func (h *Handler) previewCapabilitiesImport(c *gin.Context) {
	inputs, _, err := h.readPersonnelImport(c)
	if err != nil {
		writeCapabilityImportError(c, err)
		return
	}
	result, err := h.service.PreviewCapabilityImport(c.Request.Context(), principal(c), inputs)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, result)
}

// confirmPersonnelImport 确认导入人员资质。
// 必须携带 Idempotency-Key；选中的全部行在单事务内写入，任一行失败整批回滚（422，含行号与原因），
// 成功后记录幂等结果，同键重试返回首次响应。
func (h *Handler) confirmPersonnelImport(c *gin.Context) {
	key, err := application.NormalizeImportIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeImportIdempotencyKeyError(c, err)
		return
	}
	inputs, contentHash, err := h.readPersonnelImport(c)
	if err != nil {
		writeCapabilityImportError(c, err)
		return
	}
	selectedRows, err := parseImportSelection(c, importRowNumbers(inputs))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	preview, err := h.service.PreviewCapabilityImport(c.Request.Context(), principal(c), inputs)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	requestHash := application.ImportRequestHash(application.ImportOperationCapability, contentHash, c.PostForm("resource_type"), selectedRows)
	result, err := h.service.ConfirmCapabilityImport(c.Request.Context(), principal(c), key, requestHash, preview.Rows, selectedRows)
	if err != nil {
		writeCapabilityImportError(c, err)
		return
	}
	writeData(c, http.StatusOK, result)
}

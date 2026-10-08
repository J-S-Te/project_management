package httpapi

import (
	"bytes"
	"encoding/csv"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestCapabilityTemplateDownloadUsesSupportedHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	(&Handler{}).capabilityImportTemplate(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d", recorder.Code)
	}
	data := recorder.Body.Bytes()
	if !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		t.Fatal("Excel-friendly UTF-8 BOM missing")
	}
	rows, err := csv.NewReader(bytes.NewReader(data[3:])).ReadAll()
	if err != nil || len(rows) != 1 || len(rows[0]) != 5 {
		t.Fatalf("invalid template: %v, %v", rows, err)
	}
	for index, header := range personnelImportHeaders {
		if rows[0][index] != header {
			t.Fatalf("wrong header: %v", rows[0])
		}
	}
}

func TestPersonnelImportTemplateParser(t *testing.T) {
	rows, err := personnelImportInputs([][]string{personnelImportHeaders, {"张三", "检测部", "QUAL-1;C1", "有效", ""}})
	if err != nil || len(rows) != 1 || rows[0].RowNo != 2 || rows[0].Capability.Status != "ACTIVE" || rows[0].Capability.UserID != "" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, records := range [][][]string{{personnelImportHeaders}, {personnelImportHeaders, {"张三"}}, {{"坏表头"}, {"数据"}}} {
		if _, err := personnelImportInputs(records); err == nil {
			t.Fatalf("accepted %v", records)
		}
	}
}

func TestCapabilityImportGatewayFailureIsServiceUnavailable(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	writeCapabilityImportError(c, errCapabilityImportGateway)
	if recorder.Code != 503 {
		t.Fatalf("status=%d", recorder.Code)
	}
	rows, err := personnelImportInputs([][]string{personnelImportHeaders, {"张三", "检测部", "QUAL-1", "停用", ""}})
	if err != nil || rows[0].Capability.Status != "DISABLED" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
func TestPersonnelImportLegacyHeaderRemainsSupported(t *testing.T) {
	rows, err := personnelImportInputs([][]string{capabilityImportColumns, {"PERSON", "P-001", "张三", "QUAL-1", "ACTIVE", "not-a-date", "ignored"}})
	if err != nil || len(rows) != 1 || !rows[0].Capability.ValidUntil.IsZero() {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

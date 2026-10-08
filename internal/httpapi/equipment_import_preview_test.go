package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

type equipmentImportGateway struct{ fail bool }

func (g equipmentImportGateway) UploadEvidence(context.Context, string, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error) {
	return domain.ReportArtifactInput{}, nil
}
func (g equipmentImportGateway) UploadImport(context.Context, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error) {
	if g.fail {
		return domain.ReportArtifactInput{}, errors.New("private upstream failure")
	}
	return domain.ReportArtifactInput{}, nil
}

func TestEquipmentImportRejectsInvalidSelectionAndFailedGateway(t *testing.T) {
	for _, test := range []struct {
		selection string
		fail      bool
		status    int
	}{{"[]", false, 422}, {"[2,2]", false, 422}, {"[999]", false, 422}, {"not-json", false, 422}, {"[2]", true, 503}} {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		file, err := writer.CreateFormFile("file", "equipment.csv")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte("设备名称,能力编码,状态,设备编号,检定开始日期,检定截止日期,使用范围\n设备,C1,有效,,,,\n")); err != nil {
			t.Fatal(err)
		}
		if err = writer.WriteField("selected_rows", test.selection); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("POST", "/equipment/import", body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		h := Handler{service: &application.Service{EvidenceFiles: equipmentImportGateway{fail: test.fail}}}
		h.confirmEquipmentImport(c)
		if recorder.Code != test.status || bytes.Contains(recorder.Body.Bytes(), []byte("private upstream")) {
			t.Fatalf("selection=%s gateway=%v status=%d response=%s", test.selection, test.fail, recorder.Code, recorder.Body.String())
		}
	}
}

func TestEquipmentTemplateAndParser(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	(&Handler{}).equipmentImportTemplate(c)
	if recorder.Code != 200 || !bytes.HasPrefix(recorder.Body.Bytes(), []byte{0xef, 0xbb, 0xbf}) {
		t.Fatal("missing template or BOM")
	}
	records, err := csv.NewReader(bytes.NewReader(recorder.Body.Bytes()[3:])).ReadAll()
	if err != nil || len(records) != 1 || len(records[0]) != 7 {
		t.Fatalf("template=%v %v", records, err)
	}
	rows, err := equipmentImportInputs([][]string{equipmentImportHeaders, {"扫描设备", "C1;C2", "有效", "", "2026-10-01", "2026-10-31", "仅在公司使用"}})
	if err != nil || len(rows) != 1 || rows[0].Capability.Status != "ACTIVE" || rows[0].Capability.UsageScope != "COMPANY_ONLY" || rows[0].Capability.ValidFrom.IsZero() {
		t.Fatalf("rows=%+v %v", rows, err)
	}
	rows, err = equipmentImportInputs([][]string{equipmentImportHeaders, {"设备", "C1", "停用", "", "2026-02-30", "not-date", "可借出"}})
	if err != nil || len(rows[0].Errors) != 2 || rows[0].Capability.Status != "DISABLED" {
		t.Fatalf("bad date=%+v %v", rows, err)
	}
	rows, err = equipmentImportInputs([][]string{capabilityImportColumns, {"EQUIPMENT", "EQ-001", "设备", "C1", "ACTIVE", "2026-10-01T00:00:00Z", ""}})
	if err != nil || len(rows) != 1 || len(rows[0].Errors) != 0 {
		t.Fatalf("legacy=%+v %v", rows, err)
	}
	for _, records := range [][][]string{{equipmentImportHeaders}, {equipmentImportHeaders, {"设备"}}, {{"resource_type", "resource_id", "resource_name", "codes", "codes"}, {"EQUIPMENT", "EQ-001", "设备", "C1", "C2"}}} {
		if _, err := equipmentImportInputs(records); err == nil {
			t.Fatalf("bad structure accepted %v", records)
		}
	}
}

func TestEquipmentImportReaderFailsClosed(t *testing.T) {
	for _, filename := range []string{"devices.csv", "devices.xlsx"} {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		file, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte("设备名称,能力编码,状态,设备编号,检定开始日期,检定截止日期,使用范围\n设备,C1,有效,,,,\n")); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("POST", "/equipment/import/preview", body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		h := Handler{service: &application.Service{}}
		_, err = h.readEquipmentImport(c)
		if filename == "devices.csv" && err != errCapabilityImportGateway {
			t.Fatalf("missing gateway not rejected: %v", err)
		}
		if filename == "devices.xlsx" && err == nil {
			t.Fatal("xlsx accepted")
		}
	}
}

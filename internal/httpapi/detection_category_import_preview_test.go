package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

type detectionImportRepo struct {
	application.Repository
	saved []domain.DetectionCategory
}

func (r *detectionImportRepo) ListDetectionCategories(context.Context, string) ([]domain.DetectionCategory, error) {
	return nil, nil
}
func (r *detectionImportRepo) ListRules(context.Context, string, string) ([]domain.Rule, error) {
	return nil, nil
}

func (r *detectionImportRepo) GetSplitPolicy(context.Context, string) (domain.SplitPolicy, error) {
	return domain.DefaultSplitPolicy(), nil
}
func (r *detectionImportRepo) SaveSplitPolicy(_ context.Context, _ string, item domain.SplitPolicy, _ string) (domain.SplitPolicy, error) {
	return item, nil
}
func (r *detectionImportRepo) SaveDetectionCategory(_ context.Context, _ string, item domain.DetectionCategory, _ string) (domain.DetectionCategory, error) {
	r.saved = append(r.saved, item)
	return item, nil
}
func (r *detectionImportRepo) DeleteDetectionCategory(context.Context, string, string) error {
	return nil
}
func (r *detectionImportRepo) ListSplitOverrides(context.Context, string) ([]domain.SplitOverride, error) {
	return nil, nil
}
func (r *detectionImportRepo) SaveSplitOverride(_ context.Context, _ string, item domain.SplitOverride, _ string) (domain.SplitOverride, error) {
	return item, nil
}
func (r *detectionImportRepo) DeleteSplitOverride(context.Context, string, int64) (domain.SplitOverride, error) {
	return domain.SplitOverride{}, nil
}

func TestDetectionCategoryConfirmSelectedValidOnly(t *testing.T) {
	content := "检测类别,默认体系要求,必备资质（默认）,必检能力码,是否特殊方法,状态\n有效类别,,,,NO,启用\n无效类别,,,,NO,未知\n未选择类别,,,,NO,启用\n"
	c, recorder := detectionImportContext(t, "categories.csv", content, "[2,3]")
	c.Set("principal", platform.Principal{TenantID: "TENANT", UserID: "USER", Permissions: map[string]bool{"project_rule.manage": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}})
	r := &detectionImportRepo{}
	h := Handler{service: &application.Service{Repo: r, EvidenceFiles: equipmentImportGateway{}}}
	h.confirmDetectionCategoryImport(c)
	if recorder.Code != 200 || len(r.saved) != 1 || r.saved[0].Category != "有效类别" || !strings.Contains(recorder.Body.String(), `"imported":1`) || !strings.Contains(recorder.Body.String(), `"skipped":1`) {
		t.Fatalf("status=%d body=%s saved=%v", recorder.Code, recorder.Body.String(), r.saved)
	}
}

func detectionImportContext(t *testing.T, filename, content, selection string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err = writer.WriteField("selected_rows", selection); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/detection-categories/import", body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	return c, recorder
}

func TestDetectionCategoryImportTemplateExample(t *testing.T) {
	for _, example := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		url := "/detection-categories/import/template"
		if example {
			url += "?example=true"
		}
		c.Request = httptest.NewRequest("GET", url, nil)
		(&Handler{}).detectionCategoryImportTemplate(c)
		if recorder.Code != 200 || !bytes.HasPrefix(recorder.Body.Bytes(), []byte{0xef, 0xbb, 0xbf}) {
			t.Fatal("missing template BOM")
		}
		records, err := csv.NewReader(bytes.NewReader(recorder.Body.Bytes()[3:])).ReadAll()
		expected := 1
		if example {
			expected = 2
		}
		if err != nil || len(records) != expected {
			t.Fatalf("records=%v err=%v", records, err)
		}
		if example && (records[1][0] != "示例检测类别（请替换）" || records[1][3] != "") {
			t.Fatal("example must not copy or overwrite real category")
		}
	}
}

func TestDetectionCategoryImportSelectionAndGateway(t *testing.T) {
	content := "检测类别,默认体系要求,必备资质（默认）,必检能力码,是否特殊方法,状态\n类别,,,,NO,启用\n"
	for _, selection := range []string{"[]", "[2,2]", "[999]", "invalid"} {
		c, recorder := detectionImportContext(t, "categories.csv", content, selection)
		h := Handler{service: &application.Service{EvidenceFiles: equipmentImportGateway{}}}
		h.confirmDetectionCategoryImport(c)
		if recorder.Code != 422 {
			t.Fatalf("selection %s status %d %s", selection, recorder.Code, recorder.Body.String())
		}
	}
	for _, gateway := range []platform.EvidenceFileGateway{nil, equipmentImportGateway{fail: true}} {
		c, recorder := detectionImportContext(t, "categories.csv", content, "[2]")
		h := Handler{service: &application.Service{EvidenceFiles: gateway}}
		h.confirmDetectionCategoryImport(c)
		if recorder.Code != 503 || strings.Contains(recorder.Body.String(), "private upstream") {
			t.Fatalf("gateway status=%d %s", recorder.Code, recorder.Body.String())
		}
	}
	c, _ := detectionImportContext(t, "categories.csv", strings.Replace(content, "\n类别", "\n\n类别", 1), "")
	h := Handler{service: &application.Service{EvidenceFiles: equipmentImportGateway{}}}
	inputs, err := h.readDetectionCategoryImport(c)
	if err != nil || inputs[0].RowNo != 3 {
		t.Fatalf("physical row=%+v %v", inputs, err)
	}
}

func TestDetectionCategoryImportInputsStrictHeaderAndValues(t *testing.T) {
	rows := [][]string{append([]string{}, detectionCategoryImportHeaders...), {"代码审计", "等保 2.0", "测评师", "CODE-A", "可标记", "启用"}}
	inputs, err := detectionCategoryImportInputs(rows)
	if err != nil || len(inputs) != 1 || inputs[0].RowNo != 2 || inputs[0].Item.SpecialMethod != "MARKABLE" || !inputs[0].Item.Enabled || len(inputs[0].Errors) != 0 {
		t.Fatalf("%+v %v", inputs, err)
	}
	rows[1][5] = "任意状态"
	rows[1][4] = "任意特殊方法"
	inputs, err = detectionCategoryImportInputs(rows)
	if err != nil || len(inputs[0].Errors) != 2 {
		t.Fatalf("invalid enum silently accepted %+v %v", inputs, err)
	}
	rows[0][0] = "非法表头"
	if _, err := detectionCategoryImportInputs(rows); err == nil {
		t.Fatal("invalid header accepted")
	}
	rows[0] = append([]string{}, detectionCategoryImportHeaders...)
	rows[1] = rows[1][:5]
	if _, err := detectionCategoryImportInputs(rows); err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatal("missing column accepted")
	}
	if _, err := detectionCategoryImportInputs([][]string{detectionCategoryImportHeaders}); err == nil {
		t.Fatal("empty accepted")
	}
}

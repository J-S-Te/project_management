package httpapi

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// gatewayImportKeyPattern 是文件网关幂等键允许的形态：大写字母、数字、连字符，长度 1..255。
var gatewayImportKeyPattern = regexp.MustCompile(`^[A-Z0-9-]{1,255}$`)

func TestImportUploadRequestIDIsDeterministic(t *testing.T) {
	content := []byte("设备名称,能力编码\n设备,C1\n")
	key := importUploadRequestID("CAPABILITY", "devices.csv", content)
	if key != importUploadRequestID("CAPABILITY", "devices.csv", append([]byte(nil), content...)) {
		t.Fatal("same import type, file name and content must produce the same idempotency key")
	}
	if !gatewayImportKeyPattern.MatchString(key) || !strings.HasPrefix(key, "PMIMP-CAPABILITY-") {
		t.Fatalf("key %q does not match the gateway idempotency key format", key)
	}
	if len(key) != len("PMIMP-CAPABILITY-")+importUploadRequestIDHexLength {
		t.Fatalf("key %q length=%d", key, len(key))
	}
	for _, other := range []string{
		importUploadRequestID("CAPABILITY", "devices.csv", []byte("设备名称,能力编码\n设备,C2\n")),
		importUploadRequestID("CAPABILITY", "other.csv", content),
		importUploadRequestID("DETECTION_CATEGORY", "devices.csv", content),
	} {
		if other == key {
			t.Fatalf("key %q must change with import type, file name or content", other)
		}
		if !gatewayImportKeyPattern.MatchString(other) {
			t.Fatalf("key %q does not match the gateway idempotency key format", other)
		}
	}
	// 导入类型里的下划线等字符不能出现在键里，必须规范成网关允许的连字符。
	if segment := importUploadKeyTypeSegment("DETECTION_CATEGORY"); segment != "DETECTION-CATEGORY" {
		t.Fatalf("type segment=%q", segment)
	}
	if segment := importUploadKeyTypeSegment("   "); segment != "IMPORT" {
		t.Fatalf("blank type segment=%q", segment)
	}
}

// importKeyRepo 只满足设备能力/检测类别导入读取链路用到的最小仓储面。
// 本文件用例关心的是上传幂等键（上传发生在解析与写入之前），其余仓储方法保持零值行为；
// 确认写入交给 fakeImportStore 的内存单事务实现。
type importKeyRepo struct{ fakeImportStore }

func (importKeyRepo) FindProjectByContract(context.Context, platform.ScopeFilter, string, string) (domain.Project, error) {
	return domain.Project{}, nil
}
func (importKeyRepo) ActivateContract(context.Context, domain.Project, []domain.ServiceItem, domain.DeliveryEvent) error {
	return nil
}
func (importKeyRepo) SyncContractStampStatus(context.Context, domain.Project, bool, domain.DeliveryEvent) error {
	return nil
}
func (importKeyRepo) ApplyDeliveryEvent(context.Context, domain.DeliveryEvent) error { return nil }
func (importKeyRepo) ListSlaOverdue(context.Context, platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	return nil, nil
}
func (importKeyRepo) ListDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error) {
	return nil, nil
}
func (importKeyRepo) FindProjectForDeviation(context.Context, platform.ScopeFilter, string) (string, string, error) {
	return "", "", nil
}
func (importKeyRepo) UpsertCapability(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	return item, nil
}
func (importKeyRepo) DeleteEquipment(context.Context, string, string) error { return nil }
func (importKeyRepo) ListCapabilities(context.Context, string, string) ([]domain.Capability, error) {
	return nil, nil
}
func (importKeyRepo) FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error) {
	return nil, nil
}
func (importKeyRepo) ListEquipmentReservations(context.Context, string, string) ([]domain.EquipmentReservation, error) {
	return nil, nil
}
func (importKeyRepo) UpdateCapabilityIdentities(context.Context, string, map[string]string, time.Time) error {
	return nil
}
func (importKeyRepo) CreateEquipmentForImport(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	return item, nil
}

// ListRules 提供最小能力编码目录：C1 同时登记为设备与人员编码，
// 使预检能通过编码校验，从而真正走到确认与幂等重放路径。
func (importKeyRepo) ListRules(_ context.Context, _ string, kind string) ([]domain.Rule, error) {
	if kind != "capability-codes" {
		return nil, nil
	}
	return []domain.Rule{
		{ID: 1, Kind: kind, Scope: "C1", CheckType: "EQUIPMENT", Enabled: true},
		{ID: 2, Kind: kind, Scope: "C1", CheckType: "PERSON", Enabled: true},
	}, nil
}
func (importKeyRepo) ListDetectionCategories(context.Context, string) ([]domain.DetectionCategory, error) {
	return nil, nil
}
func (importKeyRepo) SaveDetectionCategory(_ context.Context, _ string, item domain.DetectionCategory, _ string) (domain.DetectionCategory, error) {
	return item, nil
}
func (importKeyRepo) DeleteDetectionCategory(context.Context, string, string) error { return nil }
func (importKeyRepo) GetSplitPolicy(context.Context, string) (domain.SplitPolicy, error) {
	return domain.DefaultSplitPolicy(), nil
}
func (importKeyRepo) SaveSplitPolicy(_ context.Context, _ string, item domain.SplitPolicy, _ string) (domain.SplitPolicy, error) {
	return item, nil
}
func (importKeyRepo) ListSplitOverrides(context.Context, string) ([]domain.SplitOverride, error) {
	return nil, nil
}
func (importKeyRepo) SaveSplitOverride(_ context.Context, _ string, item domain.SplitOverride, _ string) (domain.SplitOverride, error) {
	return item, nil
}
func (importKeyRepo) DeleteSplitOverride(context.Context, string, int64) (domain.SplitOverride, error) {
	return domain.SplitOverride{}, nil
}

// importRequestContext 构造一次导入请求；headerRequestID 用来证明幂等键与 X-Request-ID 无关。
func importRequestContext(t *testing.T, filename, content, selection, headerRequestID string) (*gin.Context, *httptest.ResponseRecorder) {
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
	c.Request = httptest.NewRequest("POST", "/import", body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Request.Header.Set("X-Request-ID", headerRequestID)
	c.Request.Header.Set("Idempotency-Key", "pm-import-test-0001")
	c.Set("principal", platform.Principal{TenantID: "TENANT", UserID: "USER", Permissions: map[string]bool{"project.device.manage": true, "project_rule.manage": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}})
	return c, recorder
}

// TestEquipmentImportPreviewAndConfirmShareIdempotencyKey 断言设备能力导入的预览与确认对同一份
// 文件使用同一个上传幂等键（即使两次 HTTP 请求的 X-Request-ID 不同），内容一变键就变。
func TestEquipmentImportPreviewAndConfirmShareIdempotencyKey(t *testing.T) {
	content := "设备名称,能力编码,状态,设备编号,检定开始日期,检定截止日期,使用范围\n扫描设备,C1,有效,,,,\n"
	requestIDs := []string{}
	gateway := equipmentImportGateway{requestIDs: &requestIDs}
	handler := Handler{service: &application.Service{Repo: &importKeyRepo{}, EvidenceFiles: gateway}}

	preview, previewRecorder := importRequestContext(t, "devices.csv", content, "", "01JPREVIEWEQUIPMENT00000001")
	handler.previewEquipmentImport(preview)
	confirm, confirmRecorder := importRequestContext(t, "devices.csv", content, "[2]", "01JCONFIRMEQUIPMENT00000001")
	handler.confirmEquipmentImport(confirm)

	if previewRecorder.Code != 200 || confirmRecorder.Code != 200 {
		t.Fatalf("preview=%d %s confirm=%d %s", previewRecorder.Code, previewRecorder.Body.String(), confirmRecorder.Code, confirmRecorder.Body.String())
	}
	if len(requestIDs) != 2 || requestIDs[0] != requestIDs[1] {
		t.Fatalf("preview and confirm must share the same upload idempotency key, got %v", requestIDs)
	}
	if !strings.HasPrefix(requestIDs[0], "PMIMP-CAPABILITY-") || !gatewayImportKeyPattern.MatchString(requestIDs[0]) {
		t.Fatalf("unexpected idempotency key %q", requestIDs[0])
	}

	changed, changedRecorder := importRequestContext(t, "devices.csv", strings.Replace(content, "扫描设备", "扫描仪", 1), "", "01JCHANGEDEQUIPMENT0000001")
	handler.previewEquipmentImport(changed)
	if changedRecorder.Code != 200 {
		t.Fatalf("changed preview=%d %s", changedRecorder.Code, changedRecorder.Body.String())
	}
	if len(requestIDs) != 3 || requestIDs[2] == requestIDs[0] {
		t.Fatalf("different content must produce a different key, got %v", requestIDs)
	}
}

// TestDetectionCategoryImportPreviewAndConfirmShareIdempotencyKey 覆盖检测类别导入的同一约束，
// 并断言类型前缀按网关字符集规范（DETECTION_CATEGORY → DETECTION-CATEGORY）。
func TestDetectionCategoryImportPreviewAndConfirmShareIdempotencyKey(t *testing.T) {
	content := "检测类别,默认体系要求,必备资质（默认）,必检能力码,是否特殊方法,状态\n类别A,,,,NO,启用\n"
	requestIDs := []string{}
	handler := Handler{service: &application.Service{Repo: &importKeyRepo{}, EvidenceFiles: equipmentImportGateway{requestIDs: &requestIDs}}}

	preview, previewRecorder := importRequestContext(t, "categories.csv", content, "", "01JPREVIEWDETECTION00000001")
	handler.previewDetectionCategoryImport(preview)
	confirm, confirmRecorder := importRequestContext(t, "categories.csv", content, "[2]", "01JCONFIRMDETECTION00000001")
	handler.confirmDetectionCategoryImport(confirm)

	if previewRecorder.Code != 200 || confirmRecorder.Code != 200 {
		t.Fatalf("preview=%d %s confirm=%d %s", previewRecorder.Code, previewRecorder.Body.String(), confirmRecorder.Code, confirmRecorder.Body.String())
	}
	if len(requestIDs) != 2 || requestIDs[0] != requestIDs[1] {
		t.Fatalf("preview and confirm must share the same upload idempotency key, got %v", requestIDs)
	}
	if !strings.HasPrefix(requestIDs[0], "PMIMP-DETECTION-CATEGORY-") {
		t.Fatalf("unexpected idempotency key %q", requestIDs[0])
	}

	changed, changedRecorder := importRequestContext(t, "categories.csv", strings.Replace(content, "类别A", "类别B", 1), "", "01JCHANGEDDETECTION0000001")
	handler.previewDetectionCategoryImport(changed)
	if changedRecorder.Code != 200 {
		t.Fatalf("changed preview=%d %s", changedRecorder.Code, changedRecorder.Body.String())
	}
	if len(requestIDs) != 3 || requestIDs[2] == requestIDs[0] {
		t.Fatalf("different content must produce a different key, got %v", requestIDs)
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// fakeImportStore 在内存中模拟 pm_import_operation 的「单事务 + 唯一键」语义：
// 全部行写入成功才提交并登记记录；任一行失败整批丢弃（不产生部分写入），也不登记；
// 同租户同键再次确认直接回读首次记录，由应用层比对请求指纹。
type fakeImportStore struct {
	application.Repository
	records          map[string]application.ImportOperation
	capabilities     []domain.Capability
	equipmentCreates []domain.Capability
	categories       []domain.DetectionCategory
	// failCapability / failEquipmentCreate / failCategory 按行注入失败，用于验证整批回滚。
	failCapability      func(domain.Capability) error
	failEquipmentCreate func(domain.Capability) error
	failCategory        func(domain.DetectionCategory) error
}

func (s *fakeImportStore) RunImportConfirmation(_ context.Context, op application.ImportOperation, write func(application.ImportRowWriter) (application.ImportConfirmationResult, error)) (application.ImportOperation, bool, error) {
	if s.records == nil {
		s.records = map[string]application.ImportOperation{}
	}
	key := op.TenantID + "\x00" + op.IdempotencyKey
	if record, ok := s.records[key]; ok {
		return record, true, nil
	}
	writer := &fakeImportWriter{store: s}
	result, err := write(writer)
	if err != nil {
		// 事务回滚：暂存写入全部丢弃，操作记录不登记。
		return application.ImportOperation{}, false, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return application.ImportOperation{}, false, err
	}
	op.ResponseJSON = string(payload)
	s.records[key] = op
	s.capabilities = append(s.capabilities, writer.capabilities...)
	s.equipmentCreates = append(s.equipmentCreates, writer.equipmentCreates...)
	s.categories = append(s.categories, writer.categories...)
	return op, false, nil
}

type fakeImportWriter struct {
	store            *fakeImportStore
	capabilities     []domain.Capability
	equipmentCreates []domain.Capability
	categories       []domain.DetectionCategory
}

func (w *fakeImportWriter) UpsertCapability(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	if w.store.failCapability != nil {
		if err := w.store.failCapability(item); err != nil {
			return item, err
		}
	}
	w.capabilities = append(w.capabilities, item)
	return item, nil
}

func (w *fakeImportWriter) CreateEquipmentForImport(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	if w.store.failEquipmentCreate != nil {
		if err := w.store.failEquipmentCreate(item); err != nil {
			return item, err
		}
	}
	w.equipmentCreates = append(w.equipmentCreates, item)
	return item, nil
}

func (w *fakeImportWriter) SaveDetectionCategory(_ context.Context, _ string, item domain.DetectionCategory, _ string) (domain.DetectionCategory, error) {
	if w.store.failCategory != nil {
		if err := w.store.failCategory(item); err != nil {
			return item, err
		}
	}
	w.categories = append(w.categories, item)
	return item, nil
}

// TestImportConfirmationRequiresValidIdempotencyKey 断言三个确认端点都先校验 Idempotency-Key：
// 缺失或字符集非法时返回 400，且不读取文件、不上传统一文件网关。
func TestImportConfirmationRequiresValidIdempotencyKey(t *testing.T) {
	personnelCSV := "人员姓名,组织名称,资质编码,状态,资质编号\n张三,检测部,QUAL-1,有效,\n"
	equipmentCSV := "设备名称,能力编码,状态,设备编号,检定开始日期,检定截止日期,使用范围\n扫描设备,C1,有效,,,,\n"
	categoryCSV := "检测类别,默认体系要求,必备资质（默认）,必检能力码,是否特殊方法,状态\n类别A,,,,NO,启用\n"
	requestIDs := []string{}
	gateway := equipmentImportGateway{requestIDs: &requestIDs}
	handler := Handler{service: &application.Service{Repo: &importKeyRepo{}, EvidenceFiles: gateway}}
	cases := []struct {
		name      string
		filename  string
		content   string
		selection string
		call      func(*gin.Context)
	}{
		{"personnel", "people.csv", personnelCSV, "[2]", handler.confirmPersonnelImport},
		{"equipment", "devices.csv", equipmentCSV, "[2]", handler.confirmEquipmentImport},
		{"detection", "categories.csv", categoryCSV, "[2]", handler.confirmDetectionCategoryImport},
	}
	for _, test := range cases {
		for _, key := range []string{"", "   ", "bad key", "bad/key", "bad?key"} {
			c, recorder := importRequestContext(t, test.filename, test.content, test.selection, "01JREQUESTID00000000000001")
			c.Request.Header.Set("Idempotency-Key", key)
			test.call(c)
			if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "PM_INVALID_IDEMPOTENCY_KEY") {
				t.Fatalf("%s key=%q status=%d body=%s", test.name, key, recorder.Code, recorder.Body.String())
			}
		}
	}
	if len(requestIDs) != 0 {
		t.Fatalf("idempotency key validation must run before the file gateway, uploads=%v", requestIDs)
	}
}

// TestImportConfirmationConflictsOnReusedKeyWithDifferentContent 断言同键不同文件返回 409，
// 且首次导入结果不被覆盖、也不会重复写入。
func TestImportConfirmationConflictsOnReusedKeyWithDifferentContent(t *testing.T) {
	first := "检测类别,默认体系要求,必备资质（默认）,必检能力码,是否特殊方法,状态\n类别A,,,,NO,启用\n"
	second := strings.Replace(first, "类别A", "类别B", 1)
	r := &detectionImportRepo{}
	h := Handler{service: &application.Service{Repo: r, EvidenceFiles: equipmentImportGateway{}}}
	firstRequest, firstRecorder := detectionImportContext(t, "categories.csv", first, "[2]")
	detectionImportPrincipal(firstRequest)
	h.confirmDetectionCategoryImport(firstRequest)
	if firstRecorder.Code != 200 || len(r.categories) != 1 {
		t.Fatalf("first status=%d body=%s saved=%v", firstRecorder.Code, firstRecorder.Body.String(), r.categories)
	}
	conflicting, conflictRecorder := detectionImportContext(t, "categories.csv", second, "[2]")
	detectionImportPrincipal(conflicting)
	h.confirmDetectionCategoryImport(conflicting)
	if conflictRecorder.Code != 409 || !strings.Contains(conflictRecorder.Body.String(), "PM_IMPORT_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("conflict status=%d body=%s", conflictRecorder.Code, conflictRecorder.Body.String())
	}
	if len(r.categories) != 1 || r.categories[0].Category != "类别A" {
		t.Fatalf("conflicting reuse must not overwrite the first import: %+v", r.categories)
	}
}

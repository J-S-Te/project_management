package application

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// BIZ-PROJECT-03 回归：现场记录/电子签名/报告证据必须回源文件网关核验，
// 伪造 file_id/sha256/size 的回执不能通过现场完成或报告签发门禁。

// evidenceStatCall 记录一次证据核验请求的业务对象，用于断言绑定精确到服务项。
type evidenceStatCall struct {
	FileID, ResourceType, ResourceID string
}

// evidenceGatewayStub 模拟文件网关的只读证据核验（StatEvidence）。
type evidenceGatewayStub struct {
	// metadata 是未配置 byID 时对任意文件返回的元数据。
	metadata platform.EvidenceMetadata
	// byID 按文件 ID 返回元数据；未命中按"网关认定文件不存在"拒绝。
	byID map[string]platform.EvidenceMetadata
	// bindings 记录每个文件实际绑定到的服务项；查询其它服务项时按网关真实行为拒绝。
	bindings map[string]string
	// err 模拟网关不可达/鉴权失败等依赖故障。
	err       error
	requested []string
	resources []evidenceStatCall
}

func (stub *evidenceGatewayStub) UploadEvidence(context.Context, string, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error) {
	return domain.ReportArtifactInput{}, errors.New("upload is not part of this test")
}

func (stub *evidenceGatewayStub) UploadImport(context.Context, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error) {
	return domain.ReportArtifactInput{}, errors.New("upload is not part of this test")
}

func (stub *evidenceGatewayStub) StatEvidence(_ context.Context, fileID, resourceType, resourceID string) (platform.EvidenceMetadata, error) {
	stub.requested = append(stub.requested, fileID)
	stub.resources = append(stub.resources, evidenceStatCall{FileID: fileID, ResourceType: resourceType, ResourceID: resourceID})
	if stub.err != nil {
		return platform.EvidenceMetadata{}, stub.err
	}
	if stub.bindings != nil && stub.bindings[fileID] != resourceID {
		return platform.EvidenceMetadata{}, platform.ErrEvidenceFileUnavailable
	}
	if stub.byID != nil {
		metadata, ok := stub.byID[fileID]
		if !ok {
			return platform.EvidenceMetadata{}, platform.ErrEvidenceFileUnavailable
		}
		return metadata, nil
	}
	return stub.metadata, nil
}

// verifiedEvidence 由回执生成"网关已核验"的元数据，供既有事件流测试接线使用。
func verifiedEvidence(artifact domain.ReportArtifactInput) platform.EvidenceMetadata {
	return platform.EvidenceMetadata{
		FileID: artifact.FileID, TenantID: "tenant-1", ApplicationID: "app-project", Status: "READY",
		MediaType: canonicalEvidenceMediaType(artifact.MIME), OriginalName: artifact.FileName,
		SHA256: artifact.SHA256, SizeBytes: artifact.Size, BindingActive: true, CreatedAt: time.Now().UTC(),
	}
}

// testEvidenceGateway 返回一个按回执本身回放"网关已核验"的网关桩。
func testEvidenceGateway(artifacts ...domain.ReportArtifactInput) platform.EvidenceFileGateway {
	byID := make(map[string]platform.EvidenceMetadata, len(artifacts))
	for _, artifact := range artifacts {
		byID[artifact.FileID] = verifiedEvidence(artifact)
	}
	return &evidenceGatewayStub{byID: byID}
}

func fieldEvidenceInput() domain.FieldRecordInput {
	return domain.FieldRecordInput{
		RawData: "现场测评记录", Environment: "客户现场",
		EvidenceFiles:   []domain.ReportArtifactInput{{FileID: "FILE-FIELD-9", FileName: "field.jpg", MIME: "image/jpeg", Size: 128, SHA256: strings.Repeat("b", 64)}},
		ExpectedVersion: 2,
	}
}

func fieldEvidenceService(gateway platform.EvidenceFileGateway) (*Service, *assignmentRevokeRepository) {
	repository := &assignmentRevokeRepository{item: domain.ServiceItem{ID: "SI-EVIDENCE-1", ProjectID: "PJ-EVIDENCE-1", Status: "实施中", Version: 2}}
	return &Service{Repo: repository, EvidenceFiles: gateway}, repository
}

// TestFieldRecordEvidenceMustMatchGatewayMetadata 覆盖所有"回执被篡改"的情形：
// 摘要、大小、类型、租户、绑定、文件存在性任一不匹配都必须拒绝，且不写事件。
func TestFieldRecordEvidenceMustMatchGatewayMetadata(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*platform.EvidenceMetadata)
	}{
		{name: "sha256 mismatch", mutate: func(metadata *platform.EvidenceMetadata) {
			metadata.SHA256 = strings.Repeat("c", 64)
		}},
		{name: "size mismatch", mutate: func(metadata *platform.EvidenceMetadata) { metadata.SizeBytes = 129 }},
		{name: "media type mismatch", mutate: func(metadata *platform.EvidenceMetadata) { metadata.MediaType = "application/pdf" }},
		{name: "other tenant", mutate: func(metadata *platform.EvidenceMetadata) { metadata.TenantID = "tenant-2" }},
		{name: "no active binding", mutate: func(metadata *platform.EvidenceMetadata) { metadata.BindingActive = false }},
		{name: "file no longer available", mutate: func(metadata *platform.EvidenceMetadata) { metadata.Status = "DELETED" }},
		{name: "different file id", mutate: func(metadata *platform.EvidenceMetadata) { metadata.FileID = "FILE-OTHER" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := fieldEvidenceInput()
			metadata := verifiedEvidence(input.EvidenceFiles[0])
			testCase.mutate(&metadata)
			service, repository := fieldEvidenceService(&evidenceGatewayStub{metadata: metadata})
			engineer := reportCorrectionPrincipal("engineer-1", "project.field.execute")
			err := service.SubmitFieldRecord(context.Background(), engineer, repository.item.ID, input)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
			if len(repository.events) != 0 {
				t.Fatalf("rejected evidence still wrote %d events", len(repository.events))
			}
		})
	}
}

// TestFieldRecordEvidenceAcceptedWhenGatewayConfirms 断言核验通过时事件照常落库，
// 且请求确实回源到网关（而不是本地信任回执）。
func TestFieldRecordEvidenceAcceptedWhenGatewayConfirms(t *testing.T) {
	input := fieldEvidenceInput()
	gateway := &evidenceGatewayStub{metadata: verifiedEvidence(input.EvidenceFiles[0])}
	service, repository := fieldEvidenceService(gateway)
	engineer := reportCorrectionPrincipal("engineer-1", "project.field.execute")
	if err := service.SubmitFieldRecord(context.Background(), engineer, repository.item.ID, input); err != nil {
		t.Fatalf("verified evidence rejected: %v", err)
	}
	if len(repository.events) != 1 || repository.events[0].Type != EventFieldRecordSubmitted {
		t.Fatalf("events = %+v", repository.events)
	}
	if len(gateway.requested) != 1 || gateway.requested[0] != "FILE-FIELD-9" {
		t.Fatalf("gateway calls = %+v", gateway.requested)
	}
	// 绑定必须精确到当前服务项：只传 file_id 等于默认"应用级"绑定，无法阻止跨项复用。
	if len(gateway.resources) != 1 || gateway.resources[0].ResourceType != "PROJECT_SERVICE_ITEM" || gateway.resources[0].ResourceID != repository.item.ID {
		t.Fatalf("gateway resource binding = %+v", gateway.resources)
	}
}

// TestFieldRecordEvidenceRejectsFileBoundToAnotherServiceItem 是本次修复的核心回归：
// 同一应用内，服务项 A 已绑定的证据文件不能在服务项 B 的提交里通过核验。
func TestFieldRecordEvidenceRejectsFileBoundToAnotherServiceItem(t *testing.T) {
	input := fieldEvidenceInput()
	gateway := &evidenceGatewayStub{
		metadata: verifiedEvidence(input.EvidenceFiles[0]),
		bindings: map[string]string{"FILE-FIELD-9": "SI-OTHER-ITEM"},
	}
	service, repository := fieldEvidenceService(gateway)
	engineer := reportCorrectionPrincipal("engineer-1", "project.field.execute")
	err := service.SubmitFieldRecord(context.Background(), engineer, repository.item.ID, input)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("cross-item evidence error = %v, want ErrValidation", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("cross-item evidence wrote %d events", len(repository.events))
	}
	if len(gateway.resources) != 1 || gateway.resources[0].ResourceType != "PROJECT_SERVICE_ITEM" || gateway.resources[0].ResourceID != repository.item.ID {
		t.Fatalf("gateway resource binding = %+v", gateway.resources)
	}
}

// TestFieldRecordEvidenceFailsClosedWhenGatewayUnavailable 断言网关未配置或依赖故障时
// 证据提交失败关闭：返回可重试的依赖不可用错误，不落事件，也不降级成"仅校验形状"。
func TestFieldRecordEvidenceFailsClosedWhenGatewayUnavailable(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		gateway platform.EvidenceFileGateway
	}{
		{name: "gateway not configured", gateway: nil},
		{name: "gateway unreachable", gateway: &evidenceGatewayStub{err: platform.ErrEvidenceGatewayUnavailable}},
		{name: "gateway reports unknown file", gateway: &evidenceGatewayStub{byID: map[string]platform.EvidenceMetadata{}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, repository := fieldEvidenceService(testCase.gateway)
			engineer := reportCorrectionPrincipal("engineer-1", "project.field.execute")
			err := service.SubmitFieldRecord(context.Background(), engineer, repository.item.ID, fieldEvidenceInput())
			if err == nil {
				t.Fatal("unverified evidence accepted")
			}
			if len(repository.events) != 0 {
				t.Fatalf("unverified evidence wrote %d events", len(repository.events))
			}
			if testCase.name == "gateway reports unknown file" {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("unknown file error = %v, want ErrValidation", err)
				}
				return
			}
			// 依赖不可用必须可被接口层映射成 503，而不是静默成功。
			if !errors.Is(err, ErrServiceTimeout) {
				t.Fatalf("gateway outage error = %v, want ErrServiceTimeout", err)
			}
		})
	}
}

// TestFieldSignatureRejectsArtifactWhoseGatewayTypeIsNotPNG 断言"声明 PNG、实际 PDF"
// 不能绕过电子签名的图片格式要求。
func TestFieldSignatureRejectsArtifactWhoseGatewayTypeIsNotPNG(t *testing.T) {
	repository := &assignmentRevokeRepository{item: domain.ServiceItem{ID: "SI-SIGN-9", ProjectID: "PJ-SIGN-9", Status: "实施中"}}
	signature := domain.ReportArtifactInput{FileID: "FILE-SIGN-9", FileName: "signature.png", MIME: "image/png", Size: 128, SHA256: strings.Repeat("a", 64)}
	metadata := verifiedEvidence(signature)
	metadata.MediaType = "application/pdf"
	service := &Service{Repo: repository, EvidenceFiles: &evidenceGatewayStub{metadata: metadata}}
	engineer := reportCorrectionPrincipal("engineer-1", "project.field.execute")
	err := service.CaptureFieldSignature(context.Background(), engineer, repository.item.ID, domain.FieldSignatureInput{
		SignatureFile: signature, ClientOperationID: "signature-evidence-1", CapturedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("rejected signature wrote %d events", len(repository.events))
	}
}

// reportEvidenceRepository 在交付仓储桩之上提供报告版本读写，用于报告证据门禁用例。
type reportEvidenceRepository struct {
	assignmentRevokeRepository
	revisions  []domain.ReportRevision
	registered []domain.ReportArtifactInput
}

func (r *reportEvidenceRepository) ListReportRevisions(context.Context, string, string) ([]domain.ReportRevision, error) {
	return r.revisions, nil
}

func (r *reportEvidenceRepository) RegisterReportArtifact(_ context.Context, _ string, _ string, _ uint64, input domain.ReportArtifactInput, _ string, _ time.Time) error {
	r.registered = append(r.registered, input)
	return nil
}

func reportArtifactInput() domain.ReportArtifactInput {
	return domain.ReportArtifactInput{FileID: "FILE-REPORT-9", FileName: "report.pdf", MIME: "application/pdf", Size: 2048, SHA256: strings.Repeat("d", 64)}
}

// TestReportArtifactRegistrationRequiresGatewayVerification 断言报告文件登记前必须通过
// 网关核验：伪造回执既不能登记，也不会进入审核/签发门禁。
func TestReportArtifactRegistrationRequiresGatewayVerification(t *testing.T) {
	artifact := reportArtifactInput()
	t.Run("forged receipt is rejected", func(t *testing.T) {
		repository := &reportEvidenceRepository{assignmentRevokeRepository: assignmentRevokeRepository{item: domain.ServiceItem{ID: "SI-REPORT-1", ProjectID: "PJ-REPORT-1", Status: "现场实施完成", ReportStatus: "COMPILING"}}}
		service := &Service{Repo: repository, EvidenceFiles: &evidenceGatewayStub{byID: map[string]platform.EvidenceMetadata{}}}
		preparer := reportCorrectionPrincipal("project-manager-1", "project.report.prepare")
		if err := service.RegisterReportArtifact(context.Background(), preparer, repository.item.ID, 0, artifact); !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
		if len(repository.registered) != 0 {
			t.Fatalf("forged report artifact registered: %+v", repository.registered)
		}
	})
	t.Run("verified receipt is registered", func(t *testing.T) {
		repository := &reportEvidenceRepository{assignmentRevokeRepository: assignmentRevokeRepository{item: domain.ServiceItem{ID: "SI-REPORT-1", ProjectID: "PJ-REPORT-1", Status: "现场实施完成", ReportStatus: "COMPILING"}}}
		service := &Service{Repo: repository, EvidenceFiles: &evidenceGatewayStub{metadata: verifiedEvidence(artifact)}}
		preparer := reportCorrectionPrincipal("project-manager-1", "project.report.prepare")
		if err := service.RegisterReportArtifact(context.Background(), preparer, repository.item.ID, 0, artifact); err != nil {
			t.Fatalf("verified report artifact rejected: %v", err)
		}
		if len(repository.registered) != 1 || repository.registered[0].FileID != artifact.FileID {
			t.Fatalf("registered = %+v", repository.registered)
		}
	})
}

// TestReportReviewGateReverifiesRegisteredArtifact 断言审核/签发推进前会重新核验当前
// 报告版本的文件：登记后文件被删除或替换时门禁同样拒绝。
func TestReportReviewGateReverifiesRegisteredArtifact(t *testing.T) {
	artifact := reportArtifactInput()
	revision := domain.ReportRevision{Revision: 0, Status: "COMPILING", ValidityStatus: "ACTIVE", FileID: artifact.FileID, FileName: artifact.FileName, FileMIME: artifact.MIME, FileSize: artifact.Size, FileSHA256: artifact.SHA256}
	newRepository := func() *reportEvidenceRepository {
		return &reportEvidenceRepository{
			assignmentRevokeRepository: assignmentRevokeRepository{item: domain.ServiceItem{ID: "SI-REPORT-2", ProjectID: "PJ-REPORT-2", Status: "现场实施完成", ReportStatus: "COMPILING", ReportRevision: 0}},
			revisions:                  []domain.ReportRevision{revision},
		}
	}
	t.Run("file no longer verifiable", func(t *testing.T) {
		repository := newRepository()
		service := &Service{Repo: repository, EvidenceFiles: &evidenceGatewayStub{err: platform.ErrEvidenceGatewayUnavailable}}
		reviewer := reportCorrectionPrincipal("quality-manager-1", "project.report.review")
		if err := service.UpdateReportStatus(context.Background(), reviewer, repository.item.ID, domain.ReportStatusInput{Phase: "REVIEWED"}); !errors.Is(err, ErrServiceTimeout) {
			t.Fatalf("error = %v, want ErrServiceTimeout", err)
		}
		if len(repository.events) != 0 {
			t.Fatalf("review advanced without verification: %+v", repository.events)
		}
	})
	t.Run("file metadata changes after registration", func(t *testing.T) {
		repository := newRepository()
		metadata := verifiedEvidence(artifact)
		metadata.SHA256 = strings.Repeat("e", 64)
		service := &Service{Repo: repository, EvidenceFiles: &evidenceGatewayStub{metadata: metadata}}
		reviewer := reportCorrectionPrincipal("quality-manager-1", "project.report.review")
		if err := service.UpdateReportStatus(context.Background(), reviewer, repository.item.ID, domain.ReportStatusInput{Phase: "REVIEWED"}); !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
		if len(repository.events) != 0 {
			t.Fatalf("review advanced with changed artifact: %+v", repository.events)
		}
	})
	t.Run("verified artifact advances review", func(t *testing.T) {
		repository := newRepository()
		service := &Service{Repo: repository, EvidenceFiles: &evidenceGatewayStub{metadata: verifiedEvidence(artifact)}}
		reviewer := reportCorrectionPrincipal("quality-manager-1", "project.report.review")
		if err := service.UpdateReportStatus(context.Background(), reviewer, repository.item.ID, domain.ReportStatusInput{Phase: "REVIEWED"}); err != nil {
			t.Fatalf("verified report review rejected: %v", err)
		}
		if len(repository.events) != 1 || repository.events[0].Type != EventReportStatusUpdated {
			t.Fatalf("events = %+v", repository.events)
		}
	})
}

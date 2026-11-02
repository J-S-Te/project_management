package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

const maxEvidenceBytes int64 = 20 << 20

var (
	// ErrEvidenceFileUnavailable 表示文件网关明确回答：该文件不存在、不可用或不属于
	// 当前应用（统一的 404/409/422）。业务层据此按"证据无效"拒绝，而不是当作网络故障重试。
	ErrEvidenceFileUnavailable = errors.New("file gateway reports the evidence file is unavailable")
	// ErrEvidenceGatewayUnavailable 表示文件网关不可达、鉴权失败或响应不可解析。
	// 证据无法核验时必须失败关闭：业务层不得把未知状态当成"已核验"。
	ErrEvidenceGatewayUnavailable = errors.New("file gateway evidence verification is unavailable")
)

// EvidenceMetadata 是文件网关只读证据核验的返回结果。字段全部来自网关服务端解析到的
// 文件对象与当前版本，调用方不得用它反向信任客户端声明。
type EvidenceMetadata struct {
	FileID        string
	TenantID      string
	ApplicationID string
	Status        string
	MediaType     string
	OriginalName  string
	SHA256        string
	SizeBytes     uint64
	BindingActive bool
	CreatedAt     time.Time
}

type EvidenceFileGateway interface {
	UploadEvidence(context.Context, string, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error)
	UploadImport(context.Context, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error)
	// StatEvidence 只读核验一份已上传证据的真实元数据（BIZ-PROJECT-03）。
	// resourceType/resourceID 是本次提交的业务对象（服务项），网关据此做对象级绑定校验，
	// 防止同一应用内把服务项 A 的证据复用到服务项 B。
	StatEvidence(context.Context, string, string, string) (EvidenceMetadata, error)
}

type evidenceFileGateway struct {
	tokens                 *serviceClient
	baseURL, applicationID string
	client                 *http.Client
	// metadataClient 用于只读元数据核验：与上传共享同一网关与凭据，但用更短的超时，
	// 避免单次业务提交在网关卡顿时被拖住整段上传预算。
	metadataClient *http.Client
}

func NewEvidenceFileGateway(platformBaseURL, gatewayBaseURL, applicationID, clientID, secret string) EvidenceFileGateway {
	if strings.TrimSpace(platformBaseURL) == "" || strings.TrimSpace(gatewayBaseURL) == "" || strings.TrimSpace(applicationID) == "" || strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" {
		return nil
	}
	parsed, err := url.Parse(gatewayBaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil
	}
	return &evidenceFileGateway{tokens: newServiceClient(platformBaseURL, clientID, secret), baseURL: strings.TrimRight(gatewayBaseURL, "/"), applicationID: applicationID, client: &http.Client{Timeout: 10 * time.Minute}, metadataClient: &http.Client{Timeout: 15 * time.Second}}
}

// StatEvidence 回源文件网关核验证据文件：GET /api/v1/files/{file_id}/evidence-metadata
// ?resource_type=...&resource_id=...。
//
// 该调用使用平台已下发给文件网关写入器的 platform:file:download 范围（网关据此同时
// 判定文件归属的租户与应用，并要求文件确实绑定到本次提交的业务对象）。响应只含元数据，
// 不含内容或存储路径。任何非 2xx、网络错误或不可解析的响应都返回错误：调用方必须失败关闭。
func (g *evidenceFileGateway) StatEvidence(ctx context.Context, fileID, resourceType, resourceID string) (EvidenceMetadata, error) {
	fileID = strings.TrimSpace(fileID)
	resourceType = strings.TrimSpace(resourceType)
	resourceID = strings.TrimSpace(resourceID)
	if fileID == "" || resourceType == "" || resourceID == "" {
		return EvidenceMetadata{}, fmt.Errorf("%w: file id and business resource are required", ErrEvidenceFileUnavailable)
	}
	token, err := g.tokens.token(ctx, "platform:file:download")
	if err != nil {
		return EvidenceMetadata{}, fmt.Errorf("%w: %v", ErrEvidenceGatewayUnavailable, err)
	}
	query := url.Values{}
	query.Set("resource_type", resourceType)
	query.Set("resource_id", resourceID)
	endpoint := g.baseURL + "/api/v1/files/" + url.PathEscape(fileID) + "/evidence-metadata?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return EvidenceMetadata{}, fmt.Errorf("%w: %v", ErrEvidenceGatewayUnavailable, err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := g.metadataClient.Do(request)
	if err != nil {
		return EvidenceMetadata{}, fmt.Errorf("%w: %v", ErrEvidenceGatewayUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		switch response.StatusCode {
		case http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
			return EvidenceMetadata{}, fmt.Errorf("%w: gateway returned %d", ErrEvidenceFileUnavailable, response.StatusCode)
		default:
			return EvidenceMetadata{}, fmt.Errorf("%w: gateway returned %d", ErrEvidenceGatewayUnavailable, response.StatusCode)
		}
	}
	var envelope struct {
		Data struct {
			FileID        string `json:"file_id"`
			TenantID      string `json:"tenant_id"`
			ApplicationID string `json:"application_id"`
			Status        string `json:"status"`
			MediaType     string `json:"media_type"`
			OriginalName  string `json:"original_name"`
			SHA256        string `json:"sha256"`
			SizeBytes     uint64 `json:"size_bytes"`
			BindingActive bool   `json:"binding_active"`
			CreatedAt     string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return EvidenceMetadata{}, fmt.Errorf("%w: decode gateway response: %v", ErrEvidenceGatewayUnavailable, err)
	}
	data := envelope.Data
	// 文件必须归属本子系统在网关注册的应用：网关本身已按令牌应用过滤，
	// 这里再比对一次，防止网关配置错误时把其它业务的文件当作本项目证据。
	if !strings.EqualFold(strings.TrimSpace(data.ApplicationID), strings.TrimSpace(g.applicationID)) {
		return EvidenceMetadata{}, fmt.Errorf("%w: gateway returned metadata for another application", ErrEvidenceFileUnavailable)
	}
	createdAt, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(data.CreatedAt))
	if parseErr != nil {
		createdAt, parseErr = time.Parse(time.RFC3339, strings.TrimSpace(data.CreatedAt))
	}
	// 元数据本身不完整（缺失文件/租户/应用/摘要/大小）说明网关状态异常：既不能当作核验
	// 通过，也不能当作"文件无效"，按依赖不可用失败关闭。
	if strings.TrimSpace(data.FileID) == "" || strings.TrimSpace(data.TenantID) == "" || strings.TrimSpace(data.ApplicationID) == "" || strings.TrimSpace(data.SHA256) == "" || data.SizeBytes == 0 || parseErr != nil {
		return EvidenceMetadata{}, fmt.Errorf("%w: gateway metadata is incomplete", ErrEvidenceGatewayUnavailable)
	}
	return EvidenceMetadata{
		FileID: strings.TrimSpace(data.FileID), TenantID: strings.TrimSpace(data.TenantID), ApplicationID: strings.TrimSpace(data.ApplicationID),
		Status: strings.ToUpper(strings.TrimSpace(data.Status)), MediaType: strings.ToLower(strings.TrimSpace(data.MediaType)),
		OriginalName: strings.TrimSpace(data.OriginalName), SHA256: strings.ToLower(strings.TrimSpace(data.SHA256)),
		SizeBytes: data.SizeBytes, BindingActive: data.BindingActive, CreatedAt: createdAt.UTC(),
	}, nil
}

func (g *evidenceFileGateway) UploadEvidence(ctx context.Context, requestID, itemID, kind, fileName, mimeType string, source io.Reader) (domain.ReportArtifactInput, error) {
	purpose := "project.field.evidence"
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "REPORT":
		purpose = "project.report"
	case "DEVIATION", "EXCEPTION":
		purpose = "project.deviation.evidence"
	}
	return g.uploadV2(ctx, requestID, purpose, "PROJECT_SERVICE_ITEM", itemID, strings.ToUpper(strings.TrimSpace(kind))+"_EVIDENCE", fileName, mimeType, source)
}

func (g *evidenceFileGateway) UploadImport(ctx context.Context, requestID, importType, fileName, mimeType string, source io.Reader) (domain.ReportArtifactInput, error) {
	purpose := "project.capability.import"
	if strings.EqualFold(strings.TrimSpace(importType), "DETECTION_CATEGORY") {
		purpose = "project.detection-category.import"
	}
	return g.uploadV2(ctx, requestID, purpose, "PROJECT_IMPORT", requestID, strings.ToUpper(strings.TrimSpace(importType))+"_SOURCE", fileName, mimeType, source)
}

func (g *evidenceFileGateway) uploadV2(ctx context.Context, requestID, purpose, resourceType, resourceID, bindingType, fileName, mimeType string, source io.Reader) (domain.ReportArtifactInput, error) {
	if source == nil || strings.TrimSpace(resourceID) == "" || strings.TrimSpace(fileName) == "" {
		return domain.ReportArtifactInput{}, errors.New("evidence upload input is incomplete")
	}
	temporary, err := os.CreateTemp("", "project-file-gateway-*.uploading")
	if err != nil {
		return domain.ReportArtifactInput{}, errors.New("create evidence upload staging file")
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return domain.ReportArtifactInput{}, errors.New("protect evidence upload staging file")
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, maxEvidenceBytes+1))
	if err != nil || size > maxEvidenceBytes || size == 0 {
		return domain.ReportArtifactInput{}, errors.New("evidence file must be between 1 byte and 20 MiB")
	}
	if err = temporary.Sync(); err != nil {
		return domain.ReportArtifactInput{}, errors.New("flush evidence upload staging file")
	}
	digest := hash.Sum(nil)
	token, err := g.tokens.token(ctx, "platform:file:upload")
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	createPayload, err := json.Marshal(map[string]any{
		"purpose": purpose, "original_name": fileName, "media_type": mimeType,
		"size_bytes": uint64(size), "sha256": hex.EncodeToString(digest), "classification": "INTERNAL",
		"resource_type": resourceType, "resource_id": resourceID,
		"binding_type": bindingType, "display_name": fileName,
		"idempotency_key": requestID,
	})
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/api/v2/upload-sessions", bytes.NewReader(createPayload))
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("Idempotency-Key", requestID)
	resp, err := g.client.Do(req)
	if err != nil {
		return domain.ReportArtifactInput{}, errors.New("file gateway session creation failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return domain.ReportArtifactInput{}, fmt.Errorf("file gateway session creation returned %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			UploadID string `json:"upload_id"`
			FileID   string `json:"file_id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil || strings.TrimSpace(envelope.Data.FileID) == "" || strings.TrimSpace(envelope.Data.UploadID) == "" {
		return domain.ReportArtifactInput{}, errors.New("file gateway session response is invalid")
	}
	ticketReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/api/v2/upload-sessions/"+url.PathEscape(envelope.Data.UploadID)+"/tickets", nil)
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	ticketReq.Header.Set("Authorization", "Bearer "+token)
	ticketResp, err := g.client.Do(ticketReq)
	if err != nil {
		return domain.ReportArtifactInput{}, errors.New("file gateway ticket request failed")
	}
	defer ticketResp.Body.Close()
	if ticketResp.StatusCode < 200 || ticketResp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(ticketResp.Body, 4096))
		return domain.ReportArtifactInput{}, fmt.Errorf("file gateway ticket request returned %d", ticketResp.StatusCode)
	}
	var ticketEnvelope struct {
		Data struct {
			Ticket    string `json:"ticket"`
			UploadURL string `json:"upload_url"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(ticketResp.Body, 1<<20)).Decode(&ticketEnvelope); err != nil || ticketEnvelope.Data.Ticket == "" || ticketEnvelope.Data.UploadURL == "" {
		return domain.ReportArtifactInput{}, errors.New("file gateway ticket response is invalid")
	}
	if _, err = temporary.Seek(0, io.SeekStart); err != nil {
		return domain.ReportArtifactInput{}, errors.New("rewind evidence upload staging file")
	}
	uploadURL := ticketEnvelope.Data.UploadURL
	if strings.HasPrefix(uploadURL, "/file-gateway/") {
		uploadURL = strings.TrimPrefix(uploadURL, "/file-gateway")
	}
	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPut, g.baseURL+uploadURL, temporary)
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	uploadReq.ContentLength = size
	uploadReq.Header.Set("Authorization", "UploadTicket "+ticketEnvelope.Data.Ticket)
	uploadReq.Header.Set("Content-Type", mimeType)
	uploadResp, err := g.client.Do(uploadReq)
	if err != nil {
		return domain.ReportArtifactInput{}, errors.New("file gateway content upload failed")
	}
	defer uploadResp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(uploadResp.Body, 4096))
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return domain.ReportArtifactInput{}, fmt.Errorf("file gateway content upload returned %d", uploadResp.StatusCode)
	}
	return domain.ReportArtifactInput{FileID: envelope.Data.FileID, FileName: fileName, MIME: mimeType, Size: uint64(size), SHA256: hex.EncodeToString(digest)}, nil
}

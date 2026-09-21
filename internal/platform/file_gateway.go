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

type EvidenceFileGateway interface {
	UploadEvidence(context.Context, string, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error)
	UploadImport(context.Context, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error)
}

type evidenceFileGateway struct {
	tokens                 *serviceClient
	baseURL, applicationID string
	client                 *http.Client
}

func NewEvidenceFileGateway(platformBaseURL, gatewayBaseURL, applicationID, clientID, secret string) EvidenceFileGateway {
	if strings.TrimSpace(platformBaseURL) == "" || strings.TrimSpace(gatewayBaseURL) == "" || strings.TrimSpace(applicationID) == "" || strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" {
		return nil
	}
	parsed, err := url.Parse(gatewayBaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil
	}
	return &evidenceFileGateway{tokens: newServiceClient(platformBaseURL, clientID, secret), baseURL: strings.TrimRight(gatewayBaseURL, "/"), applicationID: applicationID, client: &http.Client{Timeout: 10 * time.Minute}}
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

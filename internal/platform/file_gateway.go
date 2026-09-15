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
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

const maxEvidenceBytes int64 = 20 << 20

type EvidenceFileGateway interface {
	UploadEvidence(context.Context, string, string, string, string, string, io.Reader) (domain.ReportArtifactInput, error)
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
	return &evidenceFileGateway{tokens: newServiceClient(platformBaseURL, clientID, secret), baseURL: strings.TrimRight(gatewayBaseURL, "/"), applicationID: applicationID, client: &http.Client{Timeout: 30 * time.Second}}
}

func (g *evidenceFileGateway) UploadEvidence(ctx context.Context, requestID, itemID, kind, fileName, mimeType string, source io.Reader) (domain.ReportArtifactInput, error) {
	if source == nil || strings.TrimSpace(itemID) == "" || strings.TrimSpace(fileName) == "" {
		return domain.ReportArtifactInput{}, errors.New("evidence upload input is incomplete")
	}
	data, err := io.ReadAll(io.LimitReader(source, maxEvidenceBytes+1))
	if err != nil || int64(len(data)) > maxEvidenceBytes || len(data) == 0 {
		return domain.ReportArtifactInput{}, errors.New("evidence file must be between 1 byte and 20 MiB")
	}
	digest := sha256.Sum256(data)
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	_ = writer.WriteField("application_id", g.applicationID)
	_ = writer.WriteField("classification", "INTERNAL")
	part, err := writer.CreateFormFile("file", strings.ReplaceAll(fileName, `"`, ""))
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	if _, err = part.Write(data); err != nil {
		return domain.ReportArtifactInput{}, err
	}
	if err = writer.Close(); err != nil {
		return domain.ReportArtifactInput{}, err
	}
	token, err := g.tokens.token(ctx, "platform:file:upload")
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/api/v1/files", &multipartBody)
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("Idempotency-Key", requestID)
	resp, err := g.client.Do(req)
	if err != nil {
		return domain.ReportArtifactInput{}, errors.New("file gateway upload failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return domain.ReportArtifactInput{}, fmt.Errorf("file gateway upload returned %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			FileID string `json:"file_id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil || strings.TrimSpace(envelope.Data.FileID) == "" {
		return domain.ReportArtifactInput{}, errors.New("file gateway upload response is invalid")
	}
	bindPayload, _ := json.Marshal(map[string]any{"application_id": g.applicationID, "resource_type": "PROJECT_SERVICE_ITEM", "resource_id": itemID, "binding_type": strings.ToUpper(kind) + "_EVIDENCE", "display_name": fileName})
	bindToken, err := g.tokens.token(ctx, "platform:file:bind")
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	bindReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/api/v1/files/"+url.PathEscape(envelope.Data.FileID)+"/bindings", bytes.NewReader(bindPayload))
	bindReq.Header.Set("Authorization", "Bearer "+bindToken)
	bindReq.Header.Set("Content-Type", "application/json")
	bindResp, err := g.client.Do(bindReq)
	if err != nil {
		return domain.ReportArtifactInput{}, errors.New("file gateway binding failed")
	}
	defer bindResp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(bindResp.Body, 4096))
	if bindResp.StatusCode < 200 || bindResp.StatusCode >= 300 {
		return domain.ReportArtifactInput{}, fmt.Errorf("file gateway binding returned %d", bindResp.StatusCode)
	}
	return domain.ReportArtifactInput{FileID: envelope.Data.FileID, FileName: fileName, MIME: mimeType, Size: uint64(len(data)), SHA256: hex.EncodeToString(digest[:])}, nil
}

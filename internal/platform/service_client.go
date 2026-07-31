package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/j-s-te/project-management/authz"
	"github.com/oklog/ulid/v2"
)

type AuditEvent struct{ ActorID, ActorName, Action, ResourceType, ResourceID, RequestID, Result, ReasonCode string }
type AuditReporter interface {
	Report(context.Context, AuditEvent) error
}

type serviceClient struct {
	baseURL, clientID, clientSecret string
	client                          *http.Client
	mu                              sync.Mutex
	tokens                          map[string]cachedToken
}
type cachedToken struct {
	value     string
	expiresAt time.Time
}

func newServiceClient(baseURL, clientID, clientSecret string) *serviceClient {
	return &serviceClient{baseURL: strings.TrimRight(baseURL, "/"), clientID: clientID, clientSecret: clientSecret, client: &http.Client{Timeout: 8 * time.Second}, tokens: map[string]cachedToken{}}
}

func (c *serviceClient) token(ctx context.Context, scope string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if token := c.tokens[scope]; token.value != "" && time.Until(token.expiresAt) > 30*time.Second {
		return token.value, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {scope}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.SetBasicAuth(c.clientID, c.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return "", fmt.Errorf("platform token returned %d", response.StatusCode)
	}
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if result.AccessToken == "" || !strings.EqualFold(result.TokenType, "bearer") || !containsScope(result.Scope, scope) || result.ExpiresIn <= 0 {
		return "", fmt.Errorf("platform token missing %s scope", scope)
	}
	c.tokens[scope] = cachedToken{result.AccessToken, time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)}
	return result.AccessToken, nil
}

type auditClient struct {
	service                          *serviceClient
	applicationCode, environmentCode string
}

func NewAuditReporter(baseURL, clientID, clientSecret, applicationCode, environmentCode string) AuditReporter {
	if clientID == "" || clientSecret == "" {
		return nil
	}
	return &auditClient{newServiceClient(baseURL, clientID, clientSecret), applicationCode, environmentCode}
}
func (c *auditClient) Report(ctx context.Context, event AuditEvent) error {
	token, err := c.service.token(ctx, "audit.ingest")
	if err != nil {
		return err
	}
	trace := make([]byte, 16)
	if _, err := rand.Read(trace); err != nil {
		return err
	}
	parent := make([]byte, 8)
	if _, err := rand.Read(parent); err != nil {
		return err
	}
	payload := map[string]any{"event_id": ulid.Make().String(), "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "application_code": c.applicationCode, "environment_code": c.environmentCode, "actor_type": "USER", "actor_id": event.ActorID, "actor_name": event.ActorName, "action": event.Action, "resource_type": event.ResourceType, "resource_id": event.ResourceID, "request_id": event.RequestID, "trace_id": hex.EncodeToString(trace), "correlation_id": event.RequestID, "result": event.Result, "risk_level": "LOW", "reason_code": event.ReasonCode}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.service.baseURL+"/api/v1/audit/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", event.RequestID)
	request.Header.Set("traceparent", "00-"+hex.EncodeToString(trace)+"-"+hex.EncodeToString(parent)+"-01")
	response, err := c.service.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("platform audit returned %d", response.StatusCode)
	}
	return nil
}

type CatalogSyncOptions struct {
	Enabled                                        bool
	BaseURL, ApplicationID, ClientID, ClientSecret string
}

func SyncAuthorizationCatalog(ctx context.Context, options CatalogSyncOptions) error {
	if !options.Enabled {
		return nil
	}
	var manifest map[string]any
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		return fmt.Errorf("decode permission manifest: %w", err)
	}
	client := newServiceClient(options.BaseURL, options.ClientID, options.ClientSecret)
	token, err := client.token(ctx, "authorization.catalog.sync")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(options.BaseURL, "/")+"/api/v1/applications/"+url.PathEscape(options.ApplicationID)+"/authorization-catalog", bytes.NewReader(authz.PermissionManifest))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("platform authorization catalog returned %d", response.StatusCode)
	}
	return nil
}
func containsScope(value, wanted string) bool {
	for _, item := range strings.Fields(value) {
		if item == wanted {
			return true
		}
	}
	return false
}

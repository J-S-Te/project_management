package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type ApprovedContract struct {
	ID             string `json:"id"`
	Number         string `json:"contract_number"`
	Title          string `json:"title"`
	CustomerID     string `json:"customer_id"`
	CustomerName   string `json:"customer_name"`
	Version        uint64 `json:"version"`
	Status         string `json:"status"`
	ApprovalPassed bool   `json:"approval_passed"`
}

// ApprovedContractService is the approved contract's project-facing service
// scope. It contains no contract text, pricing, contacts or file metadata.
type ApprovedContractService struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	ServiceType string `json:"service_type"`
	Site        string `json:"site"`
	Batch       string `json:"batch"`
	Category    string `json:"category"`
	System      string `json:"system"`
	SystemLevel string `json:"system_level"`
	Requirement string `json:"requirement"`
	TestMode    string `json:"test_mode"`
}

type ApprovedContractServiceCatalog struct {
	ContractID      string                    `json:"contract_id"`
	ContractVersion uint64                    `json:"contract_version"`
	ServiceItems    []ApprovedContractService `json:"service_items"`
}

type ApprovedContractVerifier interface {
	List(context.Context, int) ([]ApprovedContract, error)
	ListReferences(context.Context, string, int) ([]ApprovedContract, string, error)
	Get(context.Context, string) (ApprovedContract, error)
	GetServiceItems(context.Context, string) (ApprovedContractServiceCatalog, error)
}

type approvedContractClient struct {
	service        *serviceClient
	baseURL, scope string
}

func NewApprovedContractVerifier(platformBaseURL, endpoint, clientID, clientSecret, scope string) ApprovedContractVerifier {
	platformBaseURL, endpoint = strings.TrimRight(strings.TrimSpace(platformBaseURL), "/"), strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if platformBaseURL == "" || endpoint == "" || strings.TrimSpace(clientID) == "" || clientSecret == "" {
		return nil
	}
	if scope = strings.TrimSpace(scope); scope == "" {
		scope = "contract.approved.internal.read"
	}
	return &approvedContractClient{service: newServiceClient(platformBaseURL, clientID, clientSecret), baseURL: endpoint, scope: scope}
}

func (c *approvedContractClient) List(ctx context.Context, limit int) ([]ApprovedContract, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("limit", fmt.Sprintf("%d", limit))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil, fmt.Errorf("contract approval service returned %d", response.StatusCode)
	}
	var envelope struct {
		Code string             `json:"code"`
		Data []ApprovedContract `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Code != "OK" {
		return nil, fmt.Errorf("contract approval service returned invalid data")
	}
	if envelope.Data == nil {
		return []ApprovedContract{}, nil
	}
	return envelope.Data, nil
}

func (c *approvedContractClient) ListReferences(ctx context.Context, afterID string, limit int) ([]ApprovedContract, string, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return nil, "", err
	}
	endpoint, err := url.Parse(strings.TrimSuffix(c.baseURL, "/approved-contracts") + "/approved-contract-references")
	if err != nil {
		return nil, "", err
	}
	query := endpoint.Query()
	query.Set("limit", fmt.Sprintf("%d", limit))
	if afterID = strings.TrimSpace(afterID); afterID != "" {
		query.Set("after_id", afterID)
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil, "", fmt.Errorf("contract approval service returned %d", response.StatusCode)
	}
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			Contracts   []ApprovedContract `json:"contracts"`
			NextAfterID string             `json:"next_after_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return nil, "", err
	}
	if envelope.Code != "OK" {
		return nil, "", fmt.Errorf("contract approval service returned invalid references")
	}
	if envelope.Data.Contracts == nil {
		envelope.Data.Contracts = []ApprovedContract{}
	}
	return envelope.Data.Contracts, strings.TrimSpace(envelope.Data.NextAfterID), nil
}

func (c *approvedContractClient) Get(ctx context.Context, contractID string) (ApprovedContract, error) {
	contractID = strings.TrimSpace(contractID)
	if contractID == "" {
		return ApprovedContract{}, fmt.Errorf("contract id is empty")
	}
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return ApprovedContract{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/"+url.PathEscape(contractID), nil)
	if err != nil {
		return ApprovedContract{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return ApprovedContract{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return ApprovedContract{}, fmt.Errorf("contract approval service returned %d", response.StatusCode)
	}
	var envelope struct {
		Code string           `json:"code"`
		Data ApprovedContract `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&envelope); err != nil {
		return ApprovedContract{}, err
	}
	if envelope.Code != "OK" || envelope.Data.ID == "" {
		return ApprovedContract{}, fmt.Errorf("contract approval service returned invalid data")
	}
	return envelope.Data, nil
}

func (c *approvedContractClient) GetServiceItems(ctx context.Context, contractID string) (ApprovedContractServiceCatalog, error) {
	contractID = strings.TrimSpace(contractID)
	if contractID == "" {
		return ApprovedContractServiceCatalog{}, fmt.Errorf("contract id is empty")
	}
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return ApprovedContractServiceCatalog{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/"+url.PathEscape(contractID)+"/service-items", nil)
	if err != nil {
		return ApprovedContractServiceCatalog{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return ApprovedContractServiceCatalog{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return ApprovedContractServiceCatalog{}, fmt.Errorf("contract approval service returned %d", response.StatusCode)
	}
	var envelope struct {
		Code string                         `json:"code"`
		Data ApprovedContractServiceCatalog `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return ApprovedContractServiceCatalog{}, err
	}
	if envelope.Code != "OK" || strings.TrimSpace(envelope.Data.ContractID) == "" || envelope.Data.ServiceItems == nil {
		return ApprovedContractServiceCatalog{}, fmt.Errorf("contract approval service returned invalid service items")
	}
	return envelope.Data, nil
}

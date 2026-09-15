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

type ApprovedContractVerifier interface {
	List(context.Context, int) ([]ApprovedContract, error)
	CountPendingProjects(context.Context) (int, error)
	Get(context.Context, string) (ApprovedContract, error)
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

func (c *approvedContractClient) CountPendingProjects(ctx context.Context) (int, error) {
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.baseURL, "/approved-contracts")+"/pending-projects/count", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return 0, fmt.Errorf("contract approval service returned %d", response.StatusCode)
	}
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope); err != nil {
		return 0, err
	}
	if envelope.Code != "OK" || envelope.Data.Count < 0 {
		return 0, fmt.Errorf("contract approval service returned invalid count")
	}
	return envelope.Data.Count, nil
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

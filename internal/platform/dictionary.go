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

const contractServiceTypeDictionary = "CONTRACT_SERVICE_TYPE"

// DictionaryItem 是平台业务字典中的启用项。
type DictionaryItem struct {
	Code       string `json:"code"`
	Label      string `json:"label"`
	I18nKey    string `json:"i18n_key"`
	Value      string `json:"value"`
	Tone       string `json:"tone"`
	Terminal   bool   `json:"terminal"`
	Actionable bool   `json:"actionable"`
}

// DictionaryReader 读取平台业务字典。
type DictionaryReader interface {
	List(context.Context, string) ([]DictionaryItem, error)
}

type dictionaryClient struct {
	service  *serviceClient
	endpoint string
	scope    string
}

// NewDictionary 使用现有负责人目录机器身份读取内部字典。
func NewDictionary(baseURL, clientID, clientSecret, scope string) DictionaryReader {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.TrimSpace(clientID) == "" || clientSecret == "" {
		return nil
	}
	if strings.TrimSpace(scope) == "" {
		scope = "owner_directory.read"
	}
	return &dictionaryClient{service: newServiceClient(baseURL, clientID, clientSecret), endpoint: baseURL + "/api/v1/internal/dictionaries/", scope: scope}
}

func (c *dictionaryClient) List(ctx context.Context, code string) ([]DictionaryItem, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("dictionary code is required")
	}
	endpoint := c.endpoint + url.PathEscape(code) + "/items?page_size=50"
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
	body, _ := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("platform dictionary returned %d", response.StatusCode)
	}
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			Items []DictionaryItem `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Code != "OK" {
		return nil, fmt.Errorf("platform dictionary response invalid")
	}
	return envelope.Data.Items, nil
}

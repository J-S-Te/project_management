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

const (
	contractServiceTypeDictionary  = "CONTRACT_SERVICE_TYPE"
	dictionaryPageSize             = 50
	maxDictionaryPageResponseBytes = 256 << 10
)

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

type dictionaryPage struct {
	Items    []DictionaryItem `json:"items"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Total    int64            `json:"total"`
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

// List 读取字典的完整启用项清单，供检测类别同步和保存校验使用。
// 参数：ctx 为请求上下文，code 为待查询的业务字典编码。
// 返回值：平台顺序的完整清单；任一页读取失败或分页不一致时返回错误。
func (c *dictionaryClient) List(ctx context.Context, code string) ([]DictionaryItem, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("dictionary code is required")
	}
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return nil, err
	}
	endpoint := c.endpoint + url.PathEscape(code) + "/items"
	items := make([]DictionaryItem, 0)
	var total int64
	for pageNumber := 1; ; pageNumber++ {
		page, err := c.listPage(ctx, endpoint, token, pageNumber)
		if err != nil {
			return nil, fmt.Errorf("read platform dictionary page %d: %w", pageNumber, err)
		}
		if pageNumber == 1 {
			total = page.Total
		}
		remaining := total - int64(len(items))
		if page.Total != total || int64(len(page.Items)) != min(remaining, int64(dictionaryPageSize)) {
			return nil, fmt.Errorf("platform dictionary page %d does not match the expected total", pageNumber)
		}
		items = append(items, page.Items...)
		if int64(len(items)) == total {
			return items, nil
		}
	}
}

// listPage 取回并验证平台字典的一页启用项。
// 参数：ctx 为请求上下文，endpoint 为字典项地址，token 为机器令牌，pageNumber 从 1 开始。
// 返回值：字典项与分页信息；平台拒绝、响应超限或分页格式错误时返回错误。
func (c *dictionaryClient) listPage(ctx context.Context, endpoint, token string, pageNumber int) (dictionaryPage, error) {
	endpoint = fmt.Sprintf("%s?page=%d&page_size=%d", endpoint, pageNumber, dictionaryPageSize)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return dictionaryPage{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return dictionaryPage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return dictionaryPage{}, fmt.Errorf("platform dictionary returned %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDictionaryPageResponseBytes+1))
	if err != nil {
		return dictionaryPage{}, err
	}
	if len(body) > maxDictionaryPageResponseBytes {
		return dictionaryPage{}, fmt.Errorf("platform dictionary response exceeds %d bytes", maxDictionaryPageResponseBytes)
	}
	var envelope struct {
		Code string         `json:"code"`
		Data dictionaryPage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return dictionaryPage{}, err
	}
	page := envelope.Data
	if envelope.Code != "OK" || page.Page != pageNumber ||
		page.PageSize != dictionaryPageSize || page.Total < 0 || page.Items == nil {
		return dictionaryPage{}, fmt.Errorf("platform dictionary response invalid")
	}
	return page, nil
}

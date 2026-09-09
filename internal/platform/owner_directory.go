package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	ownerDirectoryScope            = "owner_directory.read"
	ownerDirectoryPath             = "/api/v1/internal/owner-directory"
	maximumOwnerDirectoryPageSize  = 50
	maximumOwnerDirectoryRespBytes = 256 << 10
)

// OwnerDirectoryOrganization 是平台用户在负责人目录中的一条有效任职组织。
type OwnerDirectoryOrganization struct {
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	IsPrimary        bool   `json:"is_primary"`
}

// OwnerDirectoryUser 是基础平台允许本子系统查看的一名在职用户。
// 平台契约刻意不返回账号名、邮箱和手机号，避免子系统复制敏感身份数据。
type OwnerDirectoryUser struct {
	UserID        string                       `json:"user_id"`
	DisplayName   string                       `json:"display_name"`
	Organizations []OwnerDirectoryOrganization `json:"organizations"`
}

// OwnerDirectoryPage 是负责人目录的确定性分页结果。
type OwnerDirectoryPage struct {
	Items    []OwnerDirectoryUser `json:"items"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
	Total    int64                `json:"total"`
}

// OwnerDirectoryQuery 过滤基础平台负责人目录。
type OwnerDirectoryQuery struct {
	Keyword  string
	UserID   string
	Page     int
	PageSize int
}

// OwnerDirectory 查询基础平台人员目录；未配置集成时实现为 nil。
type OwnerDirectory interface {
	List(context.Context, OwnerDirectoryQuery) (OwnerDirectoryPage, error)
}

type ownerDirectoryClient struct {
	service  *serviceClient
	endpoint string
	scope    string
}

// NewOwnerDirectory 在缺少目录凭据时返回 nil，使尚未开通该集成的部署继续可用。
// endpoint 为空时回退到平台基址上的内部目录路径，兼容只下发 PLATFORM_BASE_URL 的环境。
func NewOwnerDirectory(baseURL, endpoint, clientID, clientSecret, scope string) OwnerDirectory {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" && baseURL != "" {
		endpoint = baseURL + ownerDirectoryPath
	}
	if baseURL == "" || endpoint == "" || strings.TrimSpace(clientID) == "" || clientSecret == "" {
		return nil
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = ownerDirectoryScope
	}
	return &ownerDirectoryClient{service: newServiceClient(baseURL, clientID, clientSecret), endpoint: endpoint, scope: scope}
}

func (c *ownerDirectoryClient) List(ctx context.Context, query OwnerDirectoryQuery) (OwnerDirectoryPage, error) {
	parsed, err := url.Parse(c.endpoint)
	if err != nil {
		return OwnerDirectoryPage{}, fmt.Errorf("owner directory endpoint is invalid: %w", err)
	}
	values := parsed.Query()
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		values.Set("keyword", keyword)
	}
	if userID := strings.TrimSpace(query.UserID); userID != "" {
		values.Set("user_id", userID)
	}
	if query.Page > 0 {
		values.Set("page", strconv.Itoa(query.Page))
	}
	pageSize := query.PageSize
	if pageSize > maximumOwnerDirectoryPageSize {
		pageSize = maximumOwnerDirectoryPageSize
	}
	if pageSize > 0 {
		values.Set("page_size", strconv.Itoa(pageSize))
	}
	parsed.RawQuery = values.Encode()

	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return OwnerDirectoryPage{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return OwnerDirectoryPage{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return OwnerDirectoryPage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return OwnerDirectoryPage{}, fmt.Errorf("platform owner directory returned %d", response.StatusCode)
	}
	var envelope struct {
		Code string             `json:"code"`
		Data OwnerDirectoryPage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumOwnerDirectoryRespBytes)).Decode(&envelope); err != nil {
		return OwnerDirectoryPage{}, fmt.Errorf("decode owner directory response: %w", err)
	}
	if envelope.Code != "OK" {
		return OwnerDirectoryPage{}, fmt.Errorf("platform owner directory returned %s", envelope.Code)
	}
	if envelope.Data.Items == nil {
		envelope.Data.Items = []OwnerDirectoryUser{}
	}
	return envelope.Data, nil
}

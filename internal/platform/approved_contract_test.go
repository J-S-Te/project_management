package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestApprovedContractClientListsWithMachineToken(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/oauth2/token":
			if request.FormValue("scope") != "contract.approved.internal.read" {
				t.Fatalf("scope=%q", request.FormValue("scope"))
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"machine-token","token_type":"Bearer","expires_in":3600,"scope":"contract.approved.internal.read"}`))}, nil
		case "/internal/v1/project/approved-contracts":
			if request.Header.Get("Authorization") != "Bearer machine-token" || request.URL.Query().Get("limit") != "200" {
				t.Fatalf("authorization=%q query=%q", request.Header.Get("Authorization"), request.URL.RawQuery)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"OK","data":[{"id":"C-1","contract_number":"HT-1","title":"技术服务","customer_id":"8","customer_name":"客户","version":2,"status":"approved","approval_passed":true}]}`))}, nil
		case "/internal/v1/project/approved-contract-references":
			// BIZ-XAPI-09：关键字与游标都必须作为查询参数透传给合同侧。
			if request.Header.Get("Authorization") != "Bearer machine-token" ||
				request.URL.Query().Get("limit") != "500" ||
				request.URL.Query().Get("keyword") != "HT-1" ||
				request.URL.Query().Get("after_id") != "C-0" {
				t.Fatalf("authorization=%q query=%q", request.Header.Get("Authorization"), request.URL.RawQuery)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"OK","data":{"contracts":[{"id":"C-1","contract_number":"HT-1","version":2}],"next_after_id":""}}`))}, nil
		case "/internal/v1/project/approved-contracts/C-1/service-items":
			if request.Header.Get("Authorization") != "Bearer machine-token" {
				t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"OK","data":{"contract_id":"C-1","contract_version":2,"service_items":[{"source_id":"SVC-1","name":"等级保护测评","service_type":"等保测评","site":"杭州机房","batch":"第一批","category":"等保测评","system":"核心系统","system_level":"三级","requirement":"按标准执行","test_mode":"STANDARD"}]}}`))}, nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	})
	client := &approvedContractClient{
		service: newServiceClient("http://platform.test", "project-client", "secret"),
		baseURL: "http://contract.test/internal/v1/project/approved-contracts",
		scope:   "contract.approved.internal.read",
	}
	client.service.client.Transport = transport
	items, err := client.List(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "C-1" || items[0].CustomerID != "8" || !items[0].ApprovalPassed {
		t.Fatalf("items=%+v", items)
	}
	references, nextAfterID, err := client.ListReferences(context.Background(), " HT-1 ", "C-0", 500)
	if err != nil || len(references) != 1 || references[0].ID != "C-1" || nextAfterID != "" {
		t.Fatalf("ListReferences() = %+v, %q, %v", references, nextAfterID, err)
	}
	catalog, err := client.GetServiceItems(context.Background(), "C-1")
	if err != nil || catalog.ContractVersion != 2 || len(catalog.ServiceItems) != 1 || catalog.ServiceItems[0].SystemLevel != "三级" {
		t.Fatalf("GetServiceItems() = %+v, %v", catalog, err)
	}
}

// TestApprovedContractServiceItemsResponseBounds 验证服务项客户端完整读取大目录并拒绝无效响应。
// 参数：t 为本地 HTTP 客户端回归测试上下文。
// 返回值：无。
func TestApprovedContractServiceItemsResponseBounds(t *testing.T) {
	items := make([]ApprovedContractService, 400)
	for index := range items {
		items[index] = ApprovedContractService{
			SourceID: fmt.Sprintf("SVC-%d", index), Requirement: strings.Repeat("验", 1000),
		}
	}
	catalog := ApprovedContractServiceCatalog{ContractID: "C-1", ContractVersion: 2, ServiceItems: items}
	catalogJSON, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"code":"OK","data":` + string(catalogJSON) + `}`
	smallBody := `{"code":"OK","data":{"contract_id":"C-1","contract_version":2,"service_items":[]}}`
	for _, test := range []struct {
		name string
		body string
		tail string
	}{
		{name: "over 1 MiB", body: body},
		{name: "over 16 MiB", body: smallBody, tail: strings.Repeat(" ", 16<<20)},
		{name: "trailing JSON", body: smallBody, tail: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &approvedContractClient{
				service: newServiceClient("http://platform.test", "project-client", "secret"),
				baseURL: "http://contract.test/internal/v1/project/approved-contracts",
				scope:   "contract.approved.internal.read",
			}
			client.service.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				var responseBody io.Reader
				switch request.URL.Path {
				case "/oauth2/token":
					responseBody = strings.NewReader(`{"access_token":"machine-token","token_type":"Bearer","expires_in":3600,"scope":"contract.approved.internal.read"}`)
				case "/internal/v1/project/approved-contracts/C-1/service-items":
					if request.Header.Get("Authorization") != "Bearer machine-token" {
						t.Fatal("missing machine authorization")
					}
					responseBody = io.MultiReader(strings.NewReader(test.body), strings.NewReader(test.tail))
				default:
					t.Fatalf("unexpected path %q", request.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(responseBody)}, nil
			})
			got, err := client.GetServiceItems(context.Background(), "C-1")
			if test.tail != "" {
				if err == nil || len(got.ServiceItems) != 0 {
					t.Fatalf("invalid response returned %d service items, error=%v", len(got.ServiceItems), err)
				}
				return
			}
			if err != nil || got.ContractVersion != 2 || len(got.ServiceItems) != len(items) {
				t.Fatalf("GetServiceItems returned %d items, version=%d, error=%v",
					len(got.ServiceItems), got.ContractVersion, err)
			}
			if got.ServiceItems[len(items)-1] != items[len(items)-1] {
				t.Fatal("last service item was not preserved")
			}
		})
	}
}

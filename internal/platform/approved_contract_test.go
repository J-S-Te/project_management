package platform

import (
	"context"
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
}

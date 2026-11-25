package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestDictionaryClientReadsAllPages 验证启用字典项按顺序读完，后续页失败时不返回部分目录。
// 参数：t 为测试上下文，使用平台分页协议的 HTTP 响应样本。
// 返回值：无。
func TestDictionaryClientReadsAllPages(t *testing.T) {
	for _, test := range []struct {
		name         string
		secondStatus int
	}{
		{name: "complete", secondStatus: http.StatusOK},
		{name: "second page forbidden", secondStatus: http.StatusForbidden},
		{name: "second page incomplete", secondStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewDictionary("http://platform.test", "project-client", "secret", "").(*dictionaryClient)
			pages := 0
			client.service.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/oauth2/token" {
					if request.FormValue("scope") != "owner_directory.read" {
						t.Fatal("missing dictionary token scope")
					}
					body := `{"access_token":"machine-token","token_type":"Bearer","expires_in":3600,"scope":"owner_directory.read"}`
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				if request.URL.Path != "/api/v1/internal/dictionaries/CONTRACT_SERVICE_TYPE/items" ||
					request.Header.Get("Authorization") != "Bearer machine-token" {
					t.Fatalf("unexpected dictionary request %s", request.URL.Path)
				}
				pages++
				if pages > 2 {
					t.Fatal("unexpected dictionary page after the second page")
				}
				query := request.URL.Query()
				if query.Get("page") != strconv.Itoa(pages) || query.Get("page_size") != "50" {
					t.Errorf("page request %d: %s", pages, request.URL.RawQuery)
				}
				if pages == 2 && test.secondStatus != http.StatusOK {
					return &http.Response{StatusCode: test.secondStatus, Body: http.NoBody}, nil
				}
				items := make([]DictionaryItem, 0)
				switch {
				case pages == 1:
					for i := 1; i <= 50; i++ {
						items = append(items, DictionaryItem{Code: fmt.Sprintf("SERVICE_%d", i), Value: "测评服务"})
					}
				case test.name == "complete":
					items = append(items, DictionaryItem{Code: "SERVICE_51", Value: "其他服务"})
				}
				payload, err := json.Marshal(map[string]any{"code": "OK", "data": map[string]any{
					"items": items, "page": pages, "page_size": 50, "total": 51,
				}})
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
			})
			items, err := client.List(context.Background(), "CONTRACT_SERVICE_TYPE")
			if pages != 2 {
				t.Errorf("requested %d pages, want 2", pages)
			}
			if test.name != "complete" {
				if err == nil || len(items) != 0 {
					t.Fatalf("List returned %d partial items, error=%v", len(items), err)
				}
				return
			}
			if err != nil || len(items) != 51 || items[0].Code != "SERVICE_1" || items[50].Code != "SERVICE_51" {
				t.Fatalf("List returned %d items, error=%v", len(items), err)
			}
		})
	}
}

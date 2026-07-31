package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/store"
)

func newHandler(t *testing.T, requireIdentity bool) http.Handler {
	t.Helper()
	s, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	return httpapi.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), requireIdentity)
}

func perform(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestProjectWorkflow(t *testing.T) {
	handler := newHandler(t, false)
	response := perform(handler, http.MethodPost, "/api/v1/projects", `{"name":"新项目","customer":"示例客户","contract":"HT-TEST-1","category":"等保测评","due":"2026-12-31"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Data struct{ ID, Status string } `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.ID == "" || body.Data.Status != "待拆解确认" {
		t.Fatalf("unexpected project: %+v", body.Data)
	}

	response = perform(handler, http.MethodGet, "/api/v1/projects?q=%E6%96%B0%E9%A1%B9%E7%9B%AE", "")
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("projects = %d", len(list.Data))
	}
}

func TestConfirmServiceItemsAndRuleUpdate(t *testing.T) {
	handler := newHandler(t, false)
	response := perform(handler, http.MethodPost, "/api/v1/service-items/confirm", `{"ids":["SI-0833-01"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", response.Code)
	}
	response = perform(handler, http.MethodPatch, "/api/v1/rules/4", `{"enabled":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("rule status = %d", response.Code)
	}
}

func TestIdentityHeaderCanBeRequired(t *testing.T) {
	handler := newHandler(t, true)
	response := perform(handler, http.MethodGet, "/api/v1/projects", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

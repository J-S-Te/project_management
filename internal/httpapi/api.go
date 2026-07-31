package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/store"
)

type API struct {
	store           *store.Store
	logger          *slog.Logger
	requireIdentity bool
}

func New(s *store.Store, logger *slog.Logger, requireIdentity bool) http.Handler {
	api := &API{store: s, logger: logger, requireIdentity: requireIdentity}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /api/v1/dashboard", api.auth(api.dashboard))
	mux.HandleFunc("GET /api/v1/projects", api.auth(api.listProjects))
	mux.HandleFunc("POST /api/v1/projects", api.auth(api.createProject))
	mux.HandleFunc("GET /api/v1/projects/{id}", api.auth(api.getProject))
	mux.HandleFunc("GET /api/v1/service-items", api.auth(api.listServiceItems))
	mux.HandleFunc("POST /api/v1/service-items/confirm", api.auth(api.confirmServiceItems))
	mux.HandleFunc("GET /api/v1/rules", api.auth(api.listRules))
	mux.HandleFunc("POST /api/v1/rules", api.auth(api.createRule))
	mux.HandleFunc("PATCH /api/v1/rules/{id}", api.auth(api.updateRule))
	return api.middleware(mux)
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (a *API) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.requireIdentity && strings.TrimSpace(r.Header.Get("X-Authenticated-User")) == "" {
			writeError(w, http.StatusUnauthorized, "PM_UNAUTHENTICATED", "缺少可信网关注入的用户身份")
			return
		}
		next(w, r)
	}
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (a *API) dashboard(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, a.store.Dashboard())
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, a.store.ListProjects(r.URL.Query().Get("q"), r.URL.Query().Get("status")))
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	project, err := a.store.GetProject(r.PathValue("id"))
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeData(w, http.StatusOK, project)
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var input domain.Project
	if !decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Customer) == "" || strings.TrimSpace(input.Contract) == "" {
		writeError(w, http.StatusUnprocessableEntity, "PM_VALIDATION_ERROR", "项目名称、客户和合同编号为必填项")
		return
	}
	created, err := a.store.CreateProject(input)
	if err != nil {
		a.storeError(w, err)
		return
	}
	a.audit(r, "project.create", created.ID)
	writeData(w, http.StatusCreated, created)
}

func (a *API) listServiceItems(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, a.store.ListServiceItems(r.URL.Query().Get("project_id")))
}

func (a *API) confirmServiceItems(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.IDs) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "PM_VALIDATION_ERROR", "至少选择一个服务项")
		return
	}
	items, err := a.store.ConfirmServiceItems(input.IDs)
	if err != nil {
		a.storeError(w, err)
		return
	}
	a.audit(r, "service_items.confirm", strings.Join(input.IDs, ","))
	writeData(w, http.StatusOK, items)
}

func (a *API) listRules(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, a.store.ListRules(r.URL.Query().Get("kind")))
}

func (a *API) createRule(w http.ResponseWriter, r *http.Request) {
	var input domain.Rule
	if !decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Scope) == "" {
		writeError(w, http.StatusUnprocessableEntity, "PM_VALIDATION_ERROR", "规则名称和适用范围为必填项")
		return
	}
	created, err := a.store.CreateRule(input)
	if err != nil {
		a.storeError(w, err)
		return
	}
	a.audit(r, "rule.create", strconv.FormatInt(created.ID, 10))
	writeData(w, http.StatusCreated, created)
}

func (a *API) updateRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PM_INVALID_ID", "规则编号不合法")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, http.StatusUnprocessableEntity, "PM_VALIDATION_ERROR", "enabled 为必填项")
		return
	}
	updated, err := a.store.SetRuleEnabled(id, *input.Enabled)
	if err != nil {
		a.storeError(w, err)
		return
	}
	a.audit(r, "rule.update", strconv.FormatInt(id, 10))
	writeData(w, http.StatusOK, updated)
}

func (a *API) audit(r *http.Request, action, resource string) {
	a.logger.Info("audit", "action", action, "resource", resource, "actor", r.Header.Get("X-Authenticated-User"), "request_id", r.Header.Get("X-Request-ID"))
}

func (a *API) storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "PM_NOT_FOUND", "资源不存在")
		return
	}
	a.logger.Error("store operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "PM_INTERNAL_ERROR", "服务暂不可用")
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "PM_INVALID_JSON", fmt.Sprintf("请求内容不合法：%v", err))
		return false
	}
	return true
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

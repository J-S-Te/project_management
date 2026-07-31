package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

type Identity interface {
	Authenticate(context.Context, *http.Request) (platform.Principal, error)
}
type OIDCFlow interface {
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	LogoutLocal(http.ResponseWriter, *http.Request)
}

type Handler struct {
	service  *application.Service
	identity Identity
	audit    platform.AuditReporter
	logger   *slog.Logger
}

func NewRouter(service *application.Service, identity Identity, audit platform.AuditReporter, logger *slog.Logger) *gin.Engine {
	h := &Handler{service: service, identity: identity, audit: audit, logger: logger}
	router := gin.New()
	router.Use(gin.Recovery(), requestID(), securityHeaders())
	router.GET("/healthz", func(c *gin.Context) { writeData(c, http.StatusOK, map[string]string{"status": "ok"}) })
	if flow, ok := identity.(OIDCFlow); ok {
		router.GET("/auth/login", func(c *gin.Context) { flow.Login(c.Writer, c.Request) })
		router.GET("/auth/callback", func(c *gin.Context) { flow.Callback(c.Writer, c.Request) })
		router.GET("/auth/logout", func(c *gin.Context) { flow.Logout(c.Writer, c.Request) })
		router.POST("/auth/local-logout", func(c *gin.Context) { flow.LogoutLocal(c.Writer, c.Request) })
	}
	api := router.Group("/api/v1")
	api.Use(h.authenticate(), h.auditWrites())
	api.GET("/auth/me", h.me)
	api.GET("/dashboard", require("project.read"), h.dashboard)
	api.GET("/projects", require("project.read"), h.listProjects)
	api.POST("/projects", require("project.create"), h.createProject)
	api.GET("/projects/:id", require("project.read"), h.getProject)
	api.GET("/service-items", require("project.read"), h.listServiceItems)
	api.POST("/service-items/confirm", require("service_item.confirm"), h.confirmServiceItems)
	api.GET("/rules", require("project.read"), h.listRules)
	api.POST("/rules", require("project_rule.manage"), h.createRule)
	api.PATCH("/rules/:id", require("project_rule.manage"), h.updateRule)
	return router
}

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if id == "" {
			id = ulid.Make().String()
		}
		c.Request.Header.Set("X-Request-ID", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
func (h *Handler) authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.identity == nil {
			writeError(c, http.StatusServiceUnavailable, "PM_IDENTITY_UNAVAILABLE", "身份服务未配置")
			c.Abort()
			return
		}
		p, err := h.identity.Authenticate(c.Request.Context(), c.Request)
		if err != nil {
			if errors.Is(err, platform.ErrUnauthenticated) {
				writeError(c, http.StatusUnauthorized, "PM_UNAUTHENTICATED", "项目系统登录状态已失效")
			} else {
				h.logger.Error("authenticate request", "error", err)
				writeError(c, http.StatusServiceUnavailable, "PM_IDENTITY_UNAVAILABLE", "身份服务暂不可用")
			}
			c.Abort()
			return
		}
		c.Set("principal", p)
		c.Next()
	}
}
func require(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !principal(c).Has(permission) {
			writeError(c, http.StatusForbidden, "PM_FORBIDDEN", "当前用户没有执行此操作的权限")
			c.Abort()
			return
		}
		c.Next()
	}
}
func principal(c *gin.Context) platform.Principal {
	value, _ := c.Get("principal")
	p, _ := value.(platform.Principal)
	return p
}

func (h *Handler) auditWrites() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if h.audit == nil || c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			return
		}
		p := principal(c)
		result := "SUCCESS"
		if c.Writer.Status() >= 400 {
			result = "FAILURE"
		}
		event := platform.AuditEvent{ActorID: p.UserID, ActorName: p.DisplayName, Action: "PROJECT_MANAGEMENT:" + c.Request.Method + ":" + strings.ReplaceAll(strings.Trim(c.Request.URL.Path, "/"), "/", "."), ResourceType: auditResource(c.Request.URL.Path), ResourceID: c.Param("id"), RequestID: c.GetHeader("X-Request-ID"), Result: result, ReasonCode: strconv.Itoa(c.Writer.Status())}
		if err := h.audit.Report(c.Request.Context(), event); err != nil {
			h.logger.Error("report platform audit", "error", err)
		}
	}
}
func auditResource(path string) string {
	if strings.Contains(path, "service-items") {
		return "SERVICE_ITEM"
	}
	if strings.Contains(path, "rules") {
		return "PROJECT_RULE"
	}
	return "PROJECT"
}

func (h *Handler) me(c *gin.Context) {
	p := principal(c)
	permissions := []string{}
	for code, granted := range p.Permissions {
		if granted {
			permissions = append(permissions, code)
		}
	}
	sort.Strings(permissions)
	writeData(c, http.StatusOK, map[string]any{"tenant_id": p.TenantID, "user_id": p.UserID, "display_name": p.DisplayName, "roles": p.Roles, "permissions": permissions, "role_config_hash": p.RoleConfigHash, "authz_revision": p.AuthzRevision})
}
func (h *Handler) dashboard(c *gin.Context) {
	item, err := h.service.Dashboard(c.Request.Context(), principal(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, item)
}
func (h *Handler) listProjects(c *gin.Context) {
	items, err := h.service.ListProjects(c.Request.Context(), principal(c), c.Query("q"), c.Query("status"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) getProject(c *gin.Context) {
	item, err := h.service.GetProject(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, item)
}
func (h *Handler) createProject(c *gin.Context) {
	var input domain.Project
	if !decode(c, &input) {
		return
	}
	item, err := h.service.CreateProject(c.Request.Context(), principal(c), input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, item)
}
func (h *Handler) listServiceItems(c *gin.Context) {
	items, err := h.service.ListServiceItems(c.Request.Context(), principal(c), c.Query("project_id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) confirmServiceItems(c *gin.Context) {
	var input struct {
		IDs []string `json:"ids"`
	}
	if !decode(c, &input) {
		return
	}
	items, err := h.service.ConfirmServiceItems(c.Request.Context(), principal(c), input.IDs)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) listRules(c *gin.Context) {
	items, err := h.service.ListRules(c.Request.Context(), principal(c), c.Query("kind"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) createRule(c *gin.Context) {
	var input domain.Rule
	if !decode(c, &input) {
		return
	}
	item, err := h.service.CreateRule(c.Request.Context(), principal(c), input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, item)
}
func (h *Handler) updateRule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "PM_INVALID_ID", "规则编号不合法")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(c, &input) {
		return
	}
	if input.Enabled == nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	item, err := h.service.SetRuleEnabled(c.Request.Context(), principal(c), id, *input.Enabled)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, item)
}

func decode(c *gin.Context, target any) bool {
	decoder := c.ShouldBindJSON(target)
	if decoder != nil {
		writeError(c, http.StatusBadRequest, "PM_INVALID_JSON", "请求内容不合法")
		return false
	}
	return true
}
func writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, application.ErrNotFound):
		writeError(c, http.StatusNotFound, "PM_NOT_FOUND", "资源不存在")
	case errors.Is(err, application.ErrValidation):
		writeError(c, http.StatusUnprocessableEntity, "PM_VALIDATION_ERROR", "请求参数不合法")
	default:
		writeError(c, http.StatusInternalServerError, "PM_INTERNAL_ERROR", "服务暂不可用")
	}
}
func writeData(c *gin.Context, status int, data any) { c.JSON(status, gin.H{"data": data}) }
func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"code": code, "message": message})
}

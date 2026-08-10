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

func NewRouter(service *application.Service, identity Identity, audit platform.AuditReporter, logger *slog.Logger, integrationOptions ...ContractIntegrationOptions) *gin.Engine {
	h := &Handler{service: service, identity: identity, audit: audit, logger: logger}
	router := gin.New()
	router.Use(gin.Recovery(), requestID(), securityHeaders())
	router.GET("/healthz", func(c *gin.Context) { writeData(c, http.StatusOK, map[string]string{"status": "ok"}) })
	if len(integrationOptions) > 0 {
		internal := router.Group("/internal/v1")
		internal.Use(h.authenticateContractIntegration(integrationOptions[0]))
		internal.POST("/contracts/activate", h.activateContract)
	}
	if flow, ok := identity.(OIDCFlow); ok {
		router.GET("/auth/login", func(c *gin.Context) { flow.Login(c.Writer, c.Request) })
		router.GET("/auth/callback", func(c *gin.Context) { flow.Callback(c.Writer, c.Request) })
		router.GET("/auth/logout", func(c *gin.Context) { flow.Logout(c.Writer, c.Request) })
		router.POST("/auth/local-logout", func(c *gin.Context) { flow.LogoutLocal(c.Writer, c.Request) })
		router.GET("/logged-out", loggedOut)
	}
	api := router.Group("/api/v1")
	api.Use(h.authenticate(), h.auditWrites())
	api.GET("/auth/me", h.me)
	api.GET("/dashboard", require("project.read"), h.dashboard)
	api.GET("/projects", require("project.read"), h.listProjects)
	api.POST("/projects", require("project.create"), h.createProject)
	api.POST("/contracts/activate", require("project.contract.import"), h.activateContract)
	api.GET("/projects/:id", require("project.read"), h.getProject)
	api.POST("/projects/:id/decomposition-adjustments", require("project.decomposition.manage"), h.adjustDecomposition)
	api.GET("/delivery-events", require("project.read"), h.listDeliveryEvents)
	api.POST("/projects/:id/field-complete", require("project.field.complete"), h.completeFieldImplementation)
	api.GET("/service-items", require("project.read"), h.listServiceItems)
	api.POST("/service-items/confirm", require("service_item.confirm"), h.confirmServiceItems)
	api.POST("/service-items/:id/assignment", require("project.resource.assign"), h.assignServiceItem)
	api.POST("/service-items/:id/team-assignment", require("project.team.assign"), h.assignTeam)
	api.POST("/service-items/:id/execution-assignment", require("project.execution.assign"), h.assignExecutionTeam)
	api.POST("/service-items/:id/implementation-plan", require("project.implementation.plan"), h.planImplementation)
	api.POST("/service-items/:id/preparation", require("project.implementation.plan"), h.startPreparation)
	api.POST("/service-items/:id/check-in", require("project.field.execute"), h.checkIn)
	api.POST("/service-items/:id/field-records", require("project.field.execute"), h.submitFieldRecord)
	api.POST("/service-items/:id/deviations", require("project.deviation.report"), h.reportDeviation)
	api.POST("/deviations/:id/review", require("project.deviation.review"), h.reviewDeviation)
	api.GET("/capabilities", require("project.resource.read"), h.listCapabilities)
	api.PUT("/capabilities", require("project.resource.manage"), h.upsertCapability)
	api.GET("/rules", require("project.read"), h.listRules)
	api.POST("/rules", require("project_rule.manage"), h.createRule)
	api.PATCH("/rules/:id", require("project_rule.manage"), h.updateRule)
	return router
}

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.ToUpper(strings.TrimSpace(c.GetHeader("X-Request-ID")))
		if _, err := ulid.ParseStrict(id); err != nil {
			id = ulid.Make().String()
		}
		c.Request.Header.Set("X-Request-ID", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func loggedOut(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusOK, `<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>已退出 · 项目管理系统</title>
<style>body{display:grid;place-items:center;min-height:100vh;margin:0;font:16px system-ui;background:#f4f7fb;color:#172033}main{padding:40px;border:1px solid #dbe3ef;border-radius:16px;background:white;text-align:center;box-shadow:0 12px 32px #0f172a12}a{color:#2563eb}</style>
<main><h1>已安全退出</h1><p>项目管理系统本地会话已清除。</p><a href="./">重新进入项目管理系统</a></main></html>`)
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
func (h *Handler) activateContract(c *gin.Context) {
	var input domain.ContractActivation
	if !decode(c, &input) {
		return
	}
	item, err := h.service.ActivateContract(c.Request.Context(), principal(c), input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, item)
}
func (h *Handler) adjustDecomposition(c *gin.Context) {
	var input domain.DecompositionAdjustmentInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.AdjustDecomposition(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusAccepted, map[string]string{"status": "SUPPLEMENT_REQUIRED"})
}
func (h *Handler) listDeliveryEvents(c *gin.Context) {
	items, err := h.service.ListDeliveryEvents(c.Request.Context(), principal(c), c.Query("project_id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) completeFieldImplementation(c *gin.Context) {
	if err := h.service.CompleteFieldImplementation(c.Request.Context(), principal(c), c.Param("id")); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"status": "现场实施完成"})
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
func (h *Handler) assignServiceItem(c *gin.Context) {
	var input domain.AssignmentInput
	if !decode(c, &input) {
		return
	}
	result, err := h.service.AssignServiceItem(c.Request.Context(), principal(c), c.Param("id"), input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, result)
}
func (h *Handler) assignTeam(c *gin.Context) {
	var input domain.TeamAssignmentInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.AssignTeam(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"status": "TEAM_ASSIGNED"})
}
func (h *Handler) assignExecutionTeam(c *gin.Context) {
	var input domain.ExecutionAssignmentInput
	if !decode(c, &input) {
		return
	}
	result, err := h.service.AssignExecutionTeam(c.Request.Context(), principal(c), c.Param("id"), input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, result)
}
func (h *Handler) planImplementation(c *gin.Context) {
	var input domain.ImplementationPlanInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.PlanImplementation(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"status": "待实施"})
}
func (h *Handler) startPreparation(c *gin.Context) {
	var input domain.PreparationInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.StartPreparation(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusAccepted, map[string]string{"status": "实施准备中"})
}
func (h *Handler) checkIn(c *gin.Context) {
	var input domain.CheckInInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.CheckIn(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, map[string]string{"status": "实施中"})
}
func (h *Handler) submitFieldRecord(c *gin.Context) {
	var input domain.FieldRecordInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.SubmitFieldRecord(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, map[string]string{"status": "RECORDED"})
}
func (h *Handler) reportDeviation(c *gin.Context) {
	var input domain.DeviationInput
	if !decode(c, &input) {
		return
	}
	id, err := h.service.ReportDeviation(c.Request.Context(), principal(c), c.Param("id"), input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, map[string]string{"id": id, "status": "PENDING"})
}
func (h *Handler) reviewDeviation(c *gin.Context) {
	var input domain.DeviationReviewInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.ReviewDeviation(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"status": strings.ToUpper(input.Decision)})
}
func (h *Handler) listCapabilities(c *gin.Context) {
	items, err := h.service.ListCapabilities(c.Request.Context(), principal(c), strings.ToUpper(c.Query("resource_type")))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) upsertCapability(c *gin.Context) {
	var input domain.Capability
	if !decode(c, &input) {
		return
	}
	item, err := h.service.UpsertCapability(c.Request.Context(), principal(c), input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, item)
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

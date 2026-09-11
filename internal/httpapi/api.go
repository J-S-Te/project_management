package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
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
	BackchannelLogout(http.ResponseWriter, *http.Request)
}

type Handler struct {
	service  *application.Service
	identity Identity
	audit    platform.AuditReporter
	logger   *slog.Logger
}

// RouterOptions 统一收口可选的服务间集成配置，未启用集成的调用方无需传入占位参数。
type RouterOptions struct {
	ContractIntegration  *ContractIntegrationOptions
	DashboardIntegration *DashboardIntegrationOptions
}

func NewRouter(service *application.Service, identity Identity, audit platform.AuditReporter, logger *slog.Logger, options ...RouterOptions) *gin.Engine {
	h := &Handler{service: service, identity: identity, audit: audit, logger: logger}
	router := gin.New()
	router.Use(gin.Recovery(), requestID(), securityHeaders())
	auditStatus := "disabled"
	if audit != nil {
		auditStatus = "enabled"
	}
	router.GET("/healthz", func(c *gin.Context) {
		writeData(c, http.StatusOK, map[string]string{"status": "ok", "audit": auditStatus})
	})
	router.GET("/readyz", func(c *gin.Context) {
		status := http.StatusOK
		state := "ready"
		if auditRequired() && audit == nil {
			status = http.StatusServiceUnavailable
			state = "not_ready"
		}
		writeData(c, status, map[string]string{"status": state, "audit": auditStatus})
	})
	var routerOptions RouterOptions
	if len(options) > 0 {
		routerOptions = options[0]
	}
	if integration := routerOptions.ContractIntegration; integration != nil && integration.Enabled {
		internal := router.Group("/internal/v1")
		internal.Use(h.authenticateContractIntegration(*integration), h.auditWrites())
		internal.POST("/contracts/activate", h.activateContract)
	}
	if integration := routerOptions.DashboardIntegration; integration != nil && integration.Enabled {
		daInternal := router.Group("/internal/v1")
		daInternal.Use(h.authenticateDashboardIntegration(*integration), h.auditWrites())
		daInternal.GET("/dashboard", h.dashboard)
	}
	if flow, ok := identity.(OIDCFlow); ok {
		router.GET("/auth/login", func(c *gin.Context) { flow.Login(c.Writer, c.Request) })
		router.GET("/auth/callback", func(c *gin.Context) { flow.Callback(c.Writer, c.Request) })
		router.GET("/auth/logout", func(c *gin.Context) { flow.Logout(c.Writer, c.Request) })
		router.POST("/auth/local-logout", func(c *gin.Context) { flow.LogoutLocal(c.Writer, c.Request) })
		router.POST("/auth/backchannel-logout", func(c *gin.Context) { flow.BackchannelLogout(c.Writer, c.Request) })
		router.GET("/logged-out", loggedOut)
	}
	api := router.Group("/api/v1")
	api.Use(h.authenticate(), h.auditWrites())
	api.GET("/auth/me", h.me)
	api.GET("/navigation", require("project.read"), h.navigation)
	api.GET("/dashboard", require("project.read"), h.dashboard)
	api.GET("/projects", require("project.read"), h.listProjects)
	api.POST("/projects", require("project.create"), h.createProject)
	api.POST("/contracts/activate", require("project.contract.import"), h.activateContract)
	api.GET("/projects/:id", require("project.read"), h.getProject)
	api.POST("/projects/:id/decomposition-adjustments", require("project.decomposition.manage"), h.adjustDecomposition)
	api.GET("/delivery-events", require("project.read"), h.listDeliveryEvents)
	api.GET("/delivery/sla-overdue", require("project.read"), h.listSlaOverdue)
	api.POST("/projects/:id/field-complete", require("project.field.complete"), h.completeFieldImplementation)
	api.GET("/service-items", require("project.read"), h.listServiceItems)
	// 目录与字典类只读接口统一以 project.read 为基线：这些接口只提供表单下拉选项
	// （团队负责人 / 项目经理 / 工程师 / 设备 / 能力码），参与项目工作的角色都需要渲染
	// 这些表单，而分配、指派、维护等写操作仍由各自的 assign/manage 权限单独把守。
	api.GET("/personnel", requireAny("project.read", "project.team.assign", "project.execution.assign"), h.listPersonnel)
	// 批量把已保存的 user_id 翻译成姓名：团队负责人 / 项目经理 / 工程师在界面上不得显示 ULID。
	api.GET("/personnel/names", requireAny("project.read", "project.team.assign", "project.execution.assign"), h.resolvePersonnelNames)
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
	api.GET("/capabilities", requireAny("project.read", "project.resource.read"), h.listCapabilities)
	api.PUT("/capabilities", require("project.resource.manage"), h.upsertCapability)
	api.POST("/capabilities/import", require("project.resource.manage"), h.importCapabilities)
	api.GET("/capabilities/export", require("project.resource.read"), h.exportCapabilities)
	api.GET("/equipment", requireAny("project.read", "project.device.read"), h.listEquipment)
	api.GET("/service-items/:id/equipment-reservations", requireAny("project.implementation.plan", "project.read"), h.listEquipmentReservations)
	api.POST("/service-items/:id/equipment-return", requireAny("project.implementation.plan", "project.device.manage"), h.returnEquipment)
	api.PUT("/equipment", require("project.device.manage"), h.upsertEquipment)
	api.GET("/rules", require("project.read"), h.listRules)
	api.POST("/rules", require("project_rule.manage"), h.createRule)
	api.PATCH("/rules/:id", require("project_rule.manage"), h.updateRule)
	api.PUT("/rules/:id", require("project_rule.manage"), h.updateConfigRule)
	api.POST("/service-items/:id/special-method-review", require("project.special_method.review"), h.reviewSpecialMethod)
	api.POST("/service-items/:id/report-status", require("project.field.complete"), h.updateReportStatus)
	return router
}

func auditRequired() bool {
	raw := strings.TrimSpace(os.Getenv("PLATFORM_AUDIT_REQUIRED"))
	if raw != "" {
		required, err := strconv.ParseBool(raw)
		return err == nil && required
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("PLATFORM_ENVIRONMENT_CODE")), "prod")
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
		// 面向业务控制台的 JSON API 不渲染第三方内容，安全头按"尽可能收紧"配置：
		// 禁止被任何页面/iframe 内嵌，阻止嗅探、缓存与跨站引用链条。
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
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
			if errors.Is(err, platform.ErrAuthorizationDenied) || errors.Is(err, platform.ErrInvalidAuthorization) {
				writeError(c, http.StatusForbidden, "PM_AUTHORIZATION_DENIED", "当前身份没有项目系统授权")
			} else if errors.Is(err, platform.ErrUnauthenticated) {
				writeError(c, http.StatusUnauthorized, "PM_UNAUTHENTICATED", "项目系统登录状态已失效")
			} else {
				h.logger.Error("authenticate request", "error", err)
				writeError(c, http.StatusServiceUnavailable, "PM_AUTHORIZATION_UNAVAILABLE", "授权服务暂不可用")
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

// requireAny 允许持有任一列出的权限的角色访问。用于同一份数据被多个分配角色复用
// 的只读接口（例如服务项操作台读取基础平台人员目录）。
func requireAny(permissions ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := principal(c)
		for _, permission := range permissions {
			if p.Has(permission) {
				c.Next()
				return
			}
		}
		writeError(c, http.StatusForbidden, "PM_FORBIDDEN", "当前用户没有执行此操作的权限")
		c.Abort()
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
		if h.audit == nil || skipAudit(c.Request.Method, c.Request.URL.Path) {
			return
		}
		p := principal(c)
		status := c.Writer.Status()
		event := platform.AuditEvent{ActorID: p.UserID, ActorName: p.DisplayName, Action: "PROJECT_MANAGEMENT:" + c.Request.Method + ":" + strings.ReplaceAll(strings.Trim(c.Request.URL.Path, "/"), "/", "."), ResourceType: auditResource(c.Request.URL.Path), ResourceID: c.Param("id"), RequestID: c.GetHeader("X-Request-ID"), Result: auditResult(status), RiskLevel: auditRiskLevel(c.Request.Method, c.Request.URL.Path, status), ReasonCode: strconv.Itoa(status), UserLoginIP: requestClientIP(c.Request)}
		if err := h.audit.Report(c.Request.Context(), event); err != nil {
			h.logger.Error("report platform audit", "error", err)
		}
	}
}

// auditResult 区分成功、拒绝与失败：401/403 记为 DENIED，便于识别越权/未授权尝试。
func auditResult(status int) string {
	switch {
	case status >= 200 && status < 400:
		return "SUCCESS"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "DENIED"
	default:
		return "FAILURE"
	}
}

// auditRiskLevel 计算粗略风险等级，使敏感/破坏性/被拒绝操作能在审计查询中被筛出。
func auditRiskLevel(method, path string, status int) string {
	if status >= http.StatusInternalServerError {
		return "HIGH"
	}
	lowered := strings.ToLower(path)
	if method == http.MethodDelete || containsAnyAudit(lowered,
		"delete", "approval", "approve", "reject", "sign", "password", "credential",
		"secret", "permission", "role", "authorization", "admin") {
		return "HIGH"
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "MEDIUM"
	}
	return "LOW"
}

func containsAnyAudit(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

// skipAudit 跳过普通读请求；写请求和敏感读（下载/导出/令牌签发）仍会审计。
func skipAudit(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return !sensitiveRead(path)
	default:
		return false
	}
}

// sensitiveRead 识别会暴露数据的读操作（下载、导出、嵌入令牌签发）。
func sensitiveRead(path string) bool {
	lowered := strings.ToLower(path)
	return containsAnyAudit(lowered, "embed", "download", "export")
}

// requestClientIP extracts a public client address from the managed frontend
// proxy. X-Real-IP is authoritative; the right-most XFF value is used as a
// fallback so a client-supplied left-most value cannot spoof audit records.
// Forwarding headers are only honoured when the direct peer is the trusted
// reverse proxy, so a directly reachable service cannot have its audit source
// address spoofed by client-supplied headers.
func requestClientIP(request *http.Request) string {
	if request == nil {
		return ""
	}
	if trustedProxyPeer(request.RemoteAddr) {
		if ip := publicClientIP(request.Header.Get("X-Real-IP")); ip != nil {
			return ip.String()
		}
		values := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
		for i := len(values) - 1; i >= 0; i-- {
			if ip := publicClientIP(values[i]); ip != nil {
				return ip.String()
			}
		}
	}
	remote := strings.TrimSpace(request.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if ip := publicClientIP(remote); ip != nil {
		return ip.String()
	}
	return ""
}

// trustedProxyPeer reports whether the direct peer address belongs to the
// trusted reverse proxy (loopback or private/link-local Docker gateway).
// A public peer means the service is directly reachable, so client-supplied
// forwarding headers must not be trusted.
func trustedProxyPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

func publicClientIP(value string) *netip.Addr {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return nil
	}
	return &addr
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
	writeData(c, http.StatusOK, map[string]any{"tenant_id": p.TenantID, "identity_id": p.IdentityID, "person_id": p.PersonID, "user_id": p.UserID, "display_name": p.DisplayName, "roles": p.Roles, "permissions": permissions, "data_scopes": p.DataScopes, "authorization_revision": p.AuthorizationRevision, "authz_revision": p.AuthorizationRevision, "catalog_version": p.CatalogVersion})
}

// navigation is the server-owned workbench contract. The browser may hide or order
// navigation items, but it must not infer a role from display labels or expand permissions.
func (h *Handler) navigation(c *gin.Context) {
	p := principal(c)
	sections := navigationSections(p.Roles)
	writeData(c, http.StatusOK, map[string]any{
		"roles":                  p.Roles,
		"sections":               sections,
		"default_section":        sections[0],
		"authorization_revision": p.AuthorizationRevision,
		"catalog_version":        p.CatalogVersion,
	})
}

// allNavigationSections 是本子系统前端已实现的全部工作区栏目，顺序与页面分组一致。
var allNavigationSections = []string{
	"dashboard", "monitoring",
	"projects", "decomposition",
	"allocation", "inbox", "planning", "preparation", "qualifications", "equipment", "assignments", "methods",
	"implementation", "exceptions", "standards", "reports",
	"split-rules", "warning-rules", "automations", "permissions", "sla",
}

func navigationSections(roles []string) []string {
	profiles := map[string][]string{
		// 拥有全部应用权限的管理员必须看到完整功能模块，而不是只有配置页。
		"admin":        allNavigationSections,
		"system_admin": allNavigationSections,
		// 各业务角色只看与本职责相关的栏目；服务端仍是最终授权边界。
		"business_admin":       {"projects", "decomposition", "allocation"},
		"team_lead":            {"projects", "allocation", "assignments", "implementation", "exceptions"},
		"technical_director":   {"dashboard", "monitoring", "projects", "qualifications", "methods", "exceptions", "standards"},
		"project_manager":      {"dashboard", "monitoring", "projects", "planning", "preparation", "assignments", "implementation", "reports"},
		"device_admin":         {"dashboard", "projects", "equipment"},
		"quality_manager":      {"dashboard", "monitoring", "projects", "qualifications", "split-rules", "warning-rules", "automations", "permissions", "sla"},
		"engineer":             {"projects", "implementation", "exceptions"},
		"penetration_engineer": {"projects", "planning", "implementation", "exceptions"},
	}
	seen := map[string]bool{}
	sections := make([]string, 0, len(allNavigationSections))
	for _, role := range roles {
		for _, section := range profiles[strings.ToLower(strings.TrimSpace(role))] {
			if !seen[section] {
				seen[section] = true
				sections = append(sections, section)
			}
		}
	}
	if len(sections) == 0 {
		// Unknown roles receive the least-privileged read-only entry point; endpoint
		// authorization remains the final enforcement boundary.
		return []string{"dashboard", "projects"}
	}
	return sections
}
func (h *Handler) dashboard(c *gin.Context) {
	currentPrincipal := principal(c)
	item, err := h.service.Dashboard(c.Request.Context(), currentPrincipal)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// 回显经过验签的租户，供聚合端拒绝跨租户或错误路由的响应。
	item.TenantID = currentPrincipal.TenantID
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
	var request struct {
		domain.Project
		ContractID      string                   `json:"contract_id"`
		ContractVersion string                   `json:"contract_version"`
		ServiceItems    []domain.ContractService `json:"service_items"`
	}
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.ContractID) == "" {
		writeServiceError(c, application.ErrValidation)
		return
	}
	request.Project.ContractVersion = strings.TrimSpace(request.ContractVersion)
	item, err := h.service.CreateProjectWithServiceItems(c.Request.Context(), principal(c), request.Project, request.ServiceItems)
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
	h.writeProjectStatus(c, http.StatusAccepted, c.Param("id"))
}
func (h *Handler) listDeliveryEvents(c *gin.Context) {
	items, err := h.service.ListDeliveryEvents(c.Request.Context(), principal(c), c.Query("project_id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) listSlaOverdue(c *gin.Context) {
	items, err := h.service.ListSlaOverdue(c.Request.Context(), principal(c))
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
	h.writeProjectStatus(c, http.StatusOK, c.Param("id"))
}

// writeProjectStatus 返回项目的唯一派生状态，避免写接口另起一套状态词汇。
func (h *Handler) writeProjectStatus(c *gin.Context, code int, projectID string) {
	project, err := h.service.GetProject(c.Request.Context(), principal(c), projectID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, code, map[string]string{"status": project.Status})
}
func (h *Handler) listServiceItems(c *gin.Context) {
	items, err := h.service.ListServiceItems(c.Request.Context(), principal(c), c.Query("project_id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}

// listPersonnel 把基础平台负责人目录代理给服务项操作台，前端据此渲染人员下拉框，
// 不再要求业务用户手工填写平台用户 ID。role_code 可重复或用逗号分隔，按应用角色
// 过滤候选人（团队负责人/项目经理/工程师）；role_origin 进一步限定角色授权来源
// （TEMPLATE 表示岗位授权模板产生的人），过滤规则由平台按有效授权判定。
func (h *Handler) listPersonnel(c *gin.Context) {
	page, err := optionalPositiveInt(c.Query("page"))
	if err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	pageSize, err := optionalPositiveInt(c.Query("page_size"))
	if err != nil || pageSize > 50 {
		writeServiceError(c, application.ErrValidation)
		return
	}
	result, err := h.service.ListPersonnel(c.Request.Context(), principal(c), c.Query("keyword"), c.Query("user_id"), queryRepeatedValues(c, "role_code"), queryRepeatedValues(c, "role_origin"), page, pageSize)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, result)
}

// queryRepeatedValues 归一化过滤参数，同时接受重复参数与逗号分隔两种写法。
// 取值合法性由平台按调用应用校验，这里只做拆分和去空，不维护子系统侧的取值白名单。
func queryRepeatedValues(c *gin.Context, key string) []string {
	raw := c.QueryArray(key)
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" {
				values = append(values, item)
			}
		}
	}
	return values
}

// resolvePersonnelNames 批量解析人员显示名。user_ids 为逗号分隔的平台 user_id，
// 单次上限由应用层约束；解析不到的 ID 不会出现在结果中，由前端回落到占位文案。
func (h *Handler) resolvePersonnelNames(c *gin.Context) {
	names, err := h.service.ResolvePersonnelNames(c.Request.Context(), principal(c), strings.Split(c.Query("user_ids"), ","))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]any{"names": names})
}

func optionalPositiveInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, application.ErrValidation
	}
	return parsed, nil
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

// returnEquipment 归还某台设备：写回归还时间，释放占用并让设备回到「在公司」。
func (h *Handler) returnEquipment(c *gin.Context) {
	var input struct {
		ResourceID string `json:"resource_id"`
	}
	if !decode(c, &input) {
		return
	}
	if err := h.service.ReturnEquipment(c.Request.Context(), principal(c), c.Param("id"), input.ResourceID); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"resource_id": input.ResourceID, "status": "RETURNED"})
}

// listEquipmentReservations 返回设备占用情况，供实施准备选择器置灰已被其他服务项占用的设备。
func (h *Handler) listEquipmentReservations(c *gin.Context) {
	items, err := h.service.ListEquipmentReservations(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}

func (h *Handler) listEquipment(c *gin.Context) {
	items, err := h.service.ListEquipment(c.Request.Context(), principal(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, items)
}
func (h *Handler) upsertEquipment(c *gin.Context) {
	var input domain.Capability
	if !decode(c, &input) {
		return
	}
	item, err := h.service.UpsertEquipment(c.Request.Context(), principal(c), input)
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
	item, err := h.service.SetRuleEnabled(c.Request.Context(), principal(c), c.Query("kind"), id, *input.Enabled)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, item)
}

// updateConfigRule 整行更新五套配置中的某一条（名称、启停开关与 kind 专属字段）。
func (h *Handler) updateConfigRule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "PM_INVALID_ID", "规则编号不合法")
		return
	}
	var input domain.Rule
	if !decode(c, &input) {
		return
	}
	item, err := h.service.UpdateRule(c.Request.Context(), principal(c), id, input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, item)
}

// reviewSpecialMethod 技术总监对特殊方法服务项复核，通过后才能发布渗透测试专项计划。
func (h *Handler) reviewSpecialMethod(c *gin.Context) {
	var input domain.SpecialMethodReviewInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.ReviewSpecialMethod(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"status": strings.ToUpper(strings.TrimSpace(input.Decision))})
}

// updateReportStatus 推进服务项报告状态（编制中→已审核→已签发→已归档）。
func (h *Handler) updateReportStatus(c *gin.Context) {
	var input domain.ReportStatusInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.UpdateReportStatus(c.Request.Context(), principal(c), c.Param("id"), input); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"status": strings.ToUpper(strings.TrimSpace(input.Phase))})
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
	case errors.Is(err, application.ErrForbidden), errors.Is(err, platform.ErrDataScopeDenied):
		writeError(c, http.StatusForbidden, "PM_SCOPE_FORBIDDEN", "资源不在当前授权数据范围内")
	case errors.Is(err, application.ErrNotFound):
		writeError(c, http.StatusNotFound, "PM_NOT_FOUND", "资源不存在")
	case errors.Is(err, application.ErrPrecondition):
		// 请求合法但服务项尚未走到该步骤：409 + 具体缺哪一步，而不是让用户以为填错了表单。
		writeError(c, http.StatusConflict, "PM_PRECONDITION_FAILED", serviceMessage(err, "服务项当前状态不满足该操作的前置条件"))
	case errors.Is(err, application.ErrValidation):
		// 服务层可携带字段级原因（例如缺哪个合规要素），优先展示它。
		writeError(c, http.StatusUnprocessableEntity, "PM_VALIDATION_ERROR", serviceMessage(err, "请求参数不合法"))
	case errors.Is(err, application.ErrResourceConflict):
		// 设备等资源的占用冲突必须把占用方与日期返给用户，否则无法调整时段。
		writeError(c, http.StatusConflict, "PM_RESOURCE_CONFLICT", serviceMessage(err, "该资源在所选时段已被占用"))
	case errors.Is(err, application.ErrConflict):
		writeError(c, http.StatusConflict, "PM_STATE_CONFLICT", "资源状态已被其他操作修改，请刷新后重试")
	case errors.Is(err, application.ErrServiceTimeout):
		writeError(c, http.StatusServiceUnavailable, "PM_SERVICE_TIMEOUT", "服务处理超时，请稍后重试")
	case errors.Is(err, application.ErrPersonnelUnavailable):
		writeError(c, http.StatusServiceUnavailable, "PM_PERSONNEL_UNAVAILABLE", "基础平台人员目录尚未配置或暂不可用")
	default:
		writeError(c, http.StatusInternalServerError, "PM_INTERNAL_ERROR", "服务暂不可用")
	}
}

// serviceMessage 优先使用服务层给出的用户可读原因，缺失时回落到固定的通用文案。
func serviceMessage(err error, fallback string) string {
	if reason := strings.TrimSpace(application.UserMessage(err)); reason != "" {
		return reason
	}
	return fallback
}
func writeData(c *gin.Context, status int, data any) { c.JSON(status, gin.H{"data": data}) }
func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"code": code, "message": message})
}

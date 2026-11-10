package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type ReimbursementOptions struct {
	Enabled           bool
	ReferenceVerifier platform.ClientCredentialsTokenVerifier
	SpendVerifier     platform.ClientCredentialsTokenVerifier
}

// authenticateReimbursement 验证按用途独立装配的精确机器身份并固定可信租户和环境。
// 参数：verifier 为用途专属验签器。
// 返回值：认证中间件；缺配置、错误应用或不可信令牌均失败关闭。
func (h *Handler) authenticateReimbursement(verifier platform.ClientCredentialsTokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		if verifier == nil {
			writeError(c, 503, "PM_REIMBURSEMENT_UNAVAILABLE", "报销机器认证未配置")
			c.Abort()
			return
		}
		value := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(value, "Bearer ") {
			writeError(c, 401, "PM_REIMBURSEMENT_UNAUTHORIZED", "需要专用机器身份")
			c.Abort()
			return
		}
		identity, err := verifier.VerifyClientCredentials(c.Request.Context(), strings.TrimSpace(strings.TrimPrefix(value, "Bearer ")))
		if err != nil || identity.TenantID == "" || identity.EnvironmentCode == "" || identity.ApplicationCode != "reimbursement" {
			writeError(c, 401, "PM_REIMBURSEMENT_UNAUTHORIZED", "机器身份无效")
			c.Abort()
			return
		}
		c.Set("expense_machine", identity)
		c.Next()
	}
}

// decodeExpenseJSON 限制报销协议请求大小并拒绝额外字段和多个JSON值。
// 参数：c 为HTTP上下文，out 为协议结构。
// 返回值：合法返回 true；格式或大小错误写400/413并返回 false。
func decodeExpenseJSON(c *gin.Context, out any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, domain.SpendSnapshotMaxBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(out)
	if err == nil {
		err = decoder.Decode(&struct{}{})
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var max *http.MaxBytesError
	if errors.As(err, &max) {
		writeError(c, 413, "PM_REIMBURSEMENT_TOO_LARGE", "请求超过协议大小限制")
	} else {
		writeError(c, 400, "PM_REIMBURSEMENT_INVALID_JSON", "请求格式不符合报销协议")
	}
	return false
}

// expenseMachine 返回认证中间件已验证的唯一来源身份。
// 参数：c 为通过机器身份认证的HTTP上下文。
// 返回值：可信租户和调用环境，不从查询参数或请求体推断。
func expenseMachine(c *gin.Context) platform.ServiceTokenIdentity {
	return c.MustGet("expense_machine").(platform.ServiceTokenIdentity)
}

// expenseSearch 查询可信主体当前可关联的项目和服务项。
// 参数：c 为机器代理请求。
// 返回值：无；通过HTTP输出最小资格引用。
func (h *Handler) expenseSearch(c *gin.Context) {
	var in application.ExpenseSearchInput
	if !decodeExpenseJSON(c, &in) {
		return
	}
	if in.PageSize == 0 {
		in.PageSize = 20
	}
	out, err := h.service.SearchExpenseReferences(c.Request.Context(), expenseMachine(c).TenantID, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, 200, out)
}

// expenseBatchValidate 在报销提交前重新批验完整关联集合。
// 参数：c 为带可信机器来源和草稿上下文的请求。
// 返回值：无；输出当前权威资格或明确阻断错误。
func (h *Handler) expenseBatchValidate(c *gin.Context) {
	var in application.ExpenseBatchInput
	if !decodeExpenseJSON(c, &in) {
		return
	}
	out, err := h.service.BatchValidateExpenseReferences(c.Request.Context(), expenseMachine(c).TenantID, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, 200, out)
}

// expenseRepository 从业务服务取可靠报销持久化能力，未接线时失败关闭。
// 参数：c 为当前请求。
// 返回值：完整仓储与可用标记；缺失时已输出503。
func (h *Handler) expenseRepository(c *gin.Context) (application.ExpenseRepository, bool) {
	repo, ok := h.service.Repo.(application.ExpenseRepository)
	if !ok {
		writeError(c, 503, "PM_REIMBURSEMENT_UNAVAILABLE", "报销持久化未配置")
	}
	return repo, ok
}

// expenseSpendSnapshot 验证来源边界后事务消费付款完整快照。
// 参数：c 为专用写入机器请求。
// 返回值：无；只有事务提交后才输出冻结回执。
func (h *Handler) expenseSpendSnapshot(c *gin.Context) {
	var in domain.SpendSnapshot
	if !decodeExpenseJSON(c, &in) {
		return
	}
	identity := expenseMachine(c)
	if in.TenantID != identity.TenantID || in.SourceEnvironment != identity.EnvironmentCode || in.SourceApplication != identity.ApplicationCode {
		writeError(c, 403, "PM_REIMBURSEMENT_SOURCE_DENIED", "快照来源与可信机器身份不一致")
		return
	}
	repo, ok := h.expenseRepository(c)
	if !ok {
		return
	}
	out, err := repo.ApplyExpenseSpend(c.Request.Context(), in)
	if errors.Is(err, domain.ErrSpendProtocol) {
		writeError(c, 409, "PM_REIMBURSEMENT_SNAPSHOT_CONFLICT", "支出协议冲突，需按来源完整快照对账处理")
		return
	}
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, 200, out)
}

// expensePageLimit 验证小批次只读接口的分页边界。
// 参数：c 为当前请求。
// 返回值：1到100之间的页大小；非法时写400并返回0。
func expensePageLimit(c *gin.Context) int {
	raw := c.DefaultQuery("page_size", "20")
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100 || len(c.Query("cursor")) > 512 {
		writeError(c, 400, "PM_REIMBURSEMENT_PAGINATION_INVALID", "分页参数无效")
		return 0
	}
	return n
}

// expenseReconcile 对当前来源分页重算实际物化数据并返回差异。
// 参数：c 为专用支出机器请求，租户和环境由令牌限定。
// 返回值：无；输出版本、摘要、净额和一致性。
func (h *Handler) expenseReconcile(c *gin.Context) {
	limit := expensePageLimit(c)
	if limit == 0 {
		return
	}
	repo, ok := h.expenseRepository(c)
	if !ok {
		return
	}
	identity := expenseMachine(c)
	out, err := repo.ReconcileExpenseSpend(c.Request.Context(), identity.TenantID, identity.EnvironmentCode, c.Query("cursor"), limit)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	next := ""
	if len(out) == limit {
		last := out[len(out)-1]
		raw, _ := json.Marshal(struct {
			UpdatedAt any    `json:"updated_at"`
			ClaimID   string `json:"claim_id"`
		}{last.UpdatedAt, last.ClaimID})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	writeData(c, 200, map[string]any{"items": out, "next_cursor": next})
}

// expenseRebuild 从已验证不可变快照受控恢复指定单据贡献。
// 参数：c 为专用机器请求，body仅包含单据和修复理由。
// 返回值：无；重建及修复审计提交后输出成功。
func (h *Handler) expenseRebuild(c *gin.Context) {
	var in struct {
		ClaimID string `json:"claim_id"`
		Reason  string `json:"reason"`
	}
	if !decodeExpenseJSON(c, &in) {
		return
	}
	if in.ClaimID == "" || len(in.ClaimID) > 128 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1000 {
		writeServiceError(c, application.ErrValidation)
		return
	}
	repo, ok := h.expenseRepository(c)
	if !ok {
		return
	}
	identity := expenseMachine(c)
	if err := repo.RebuildExpenseSpend(c.Request.Context(), identity.TenantID, identity.EnvironmentCode, in.ClaimID, "reimbursement:machine", in.Reason); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, 200, map[string]any{"rebuilt": true})
}

// expenseParticipants 在当前项目管理范围分页读取显式名单。
// 参数：c 为有管理权限的真实会话。
// 返回值：无；输出人员资格或范围错误。
func (h *Handler) expenseParticipants(c *gin.Context) {
	limit := expensePageLimit(c)
	if limit == 0 {
		return
	}
	scope, err := principal(c).ProjectScopeFilter()
	if err != nil {
		writeServiceError(c, application.ErrForbidden)
		return
	}
	repo, ok := h.expenseRepository(c)
	if !ok {
		return
	}
	out, err := repo.ListExpenseParticipants(c.Request.Context(), scope, c.Param("id"), c.Query("cursor"), limit)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, 200, map[string]any{"items": out})
}

// expenseManageParticipant 在管理权限、当前项目范围和人员目录下写入带版本的资格。
// 参数：c 为真实会话管理请求。
// 返回值：无；输出新版本或校验冲突。
func (h *Handler) expenseManageParticipant(c *gin.Context) {
	var in application.ExpenseParticipantInput
	if !decodeExpenseJSON(c, &in) {
		return
	}
	out, err := h.service.ManageExpenseParticipant(c.Request.Context(), principal(c), c.Param("id"), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, 200, out)
}

// expenseSpendSummary 在独立读取权限和项目范围内返回最小现金支出摘要。
// 参数：c 为真实会话读取请求。
// 返回值：无；仅输出按单据分组的净额及来源版本。
func (h *Handler) expenseSpendSummary(c *gin.Context) {
	limit := expensePageLimit(c)
	if limit == 0 {
		return
	}
	p := principal(c)
	scope, err := p.ProjectScopeFilter()
	if err != nil {
		writeServiceError(c, application.ErrForbidden)
		return
	}
	if _, err = h.service.Repo.GetProject(c.Request.Context(), scope, c.Param("id")); err != nil {
		writeServiceError(c, err)
		return
	}
	repo, ok := h.expenseRepository(c)
	if !ok {
		return
	}
	out, err := repo.ReadExpenseSpendSummary(c.Request.Context(), p.TenantID, c.Param("id"), c.Query("cursor"), limit)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, 200, map[string]any{"items": out, "metric": "paid_net_minor"})
}

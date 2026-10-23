package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/handover"
	"github.com/j-s-te/project-management/internal/platform"
)

// HandoverIntegrationOptions 配置基础平台调用的人员交接机器协议端点。
type HandoverIntegrationOptions struct {
	Enabled        bool
	BearerVerifier platform.ClientCredentialsTokenVerifier
}

const handoverTenantKey = "handover_tenant_id"

// authenticateHandoverIntegration 校验平台机器令牌并把租户绑定到请求上下文；
// 请求体或查询参数都不能扩大租户范围。
func (h *Handler) authenticateHandoverIntegration(options HandoverIntegrationOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !options.Enabled {
			writeError(c, http.StatusNotFound, "PM_HANDOVER_DISABLED", "交接接口未启用")
			c.Abort()
			return
		}
		if options.BearerVerifier == nil {
			writeError(c, http.StatusServiceUnavailable, "PM_HANDOVER_AUTH_UNAVAILABLE", "交接机器身份校验未配置")
			c.Abort()
			return
		}
		const bearerPrefix = "Bearer "
		authorization := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(authorization, bearerPrefix) || strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix)) == "" {
			writeError(c, http.StatusUnauthorized, "PM_HANDOVER_BEARER_REQUIRED", "交接请求必须携带基础平台机器访问令牌")
			c.Abort()
			return
		}
		identity, err := options.BearerVerifier.VerifyClientCredentials(c.Request.Context(), strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix)))
		if err != nil || strings.TrimSpace(identity.TenantID) == "" {
			writeError(c, http.StatusUnauthorized, "PM_HANDOVER_BEARER_INVALID", "交接机器访问令牌无效")
			c.Abort()
			return
		}
		c.Set(handoverTenantKey, strings.TrimSpace(identity.TenantID))
		c.Next()
	}
}

// handoverTenant 只接受令牌绑定的租户；请求体填写必须一致。
func handoverTenant(c *gin.Context, requested string) (string, bool) {
	tenant, ok := c.Get(handoverTenantKey)
	if !ok {
		writeError(c, http.StatusForbidden, "PM_HANDOVER_TENANT_REQUIRED", "交接请求缺少机器身份租户")
		return "", false
	}
	value := strings.TrimSpace(tenant.(string))
	if strings.TrimSpace(requested) != "" && strings.TrimSpace(requested) != value {
		writeError(c, http.StatusForbidden, "PM_HANDOVER_TENANT_MISMATCH", "请求租户与机器身份租户不一致")
		return "", false
	}
	return value, true
}

type handoverFreezePayload struct {
	TenantID  string `json:"tenant_id"`
	BatchID   string `json:"batch_id"`
	RequestID string `json:"request_id"`
	UserID    string `json:"user_id"`
}

type handoverTransferPayload struct {
	TenantID        string `json:"tenant_id"`
	BatchID         string `json:"batch_id"`
	ItemID          string `json:"item_id"`
	RequestID       string `json:"request_id"`
	ResourceType    string `json:"resource_type"`
	ResourceID      string `json:"resource_id"`
	FromUserID      string `json:"from_user_id"`
	ToUserID        string `json:"to_user_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type handoverCompletePayload struct {
	TenantID string `json:"tenant_id"`
	BatchID  string `json:"batch_id"`
	Outcome  string `json:"outcome"`
}

func (h *Handler) handoverFreeze(c *gin.Context) {
	var payload handoverFreezePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		handoverWriteError(c, handover.ErrValidation)
		return
	}
	tenant, ok := handoverTenant(c, payload.TenantID)
	if !ok {
		return
	}
	freeze, count, err := h.handover.Freeze(c.Request.Context(), tenant, payload.BatchID, payload.RequestID, payload.UserID)
	if err != nil {
		handoverWriteError(c, err)
		return
	}
	writeData(c, http.StatusOK, gin.H{"system": "project", "frozen": freeze.Status == handover.FreezeFrozen,
		"frozen_at": freeze.FrozenAt, "responsibility_count": count})
}

func (h *Handler) handoverResponsibilities(c *gin.Context) {
	tenant, ok := handoverTenant(c, c.Query("tenant_id"))
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, next, err := h.handover.Responsibilities(c.Request.Context(), tenant, c.Query("user_id"), c.Query("cursor"), limit)
	if err != nil {
		handoverWriteError(c, err)
		return
	}
	writeData(c, http.StatusOK, gin.H{"system": "project", "items": items, "next_cursor": next})
}

func (h *Handler) handoverTransfer(c *gin.Context) {
	var payload handoverTransferPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		handoverWriteError(c, handover.ErrValidation)
		return
	}
	tenant, ok := handoverTenant(c, payload.TenantID)
	if !ok {
		return
	}
	receipt, err := h.handover.Transfer(c.Request.Context(), handover.TransferInput{
		TenantID: tenant, BatchID: payload.BatchID, ItemID: payload.ItemID, RequestID: payload.RequestID,
		ResourceType: payload.ResourceType, ResourceID: payload.ResourceID,
		FromUserID: payload.FromUserID, ToUserID: payload.ToUserID,
		ExpectedVersion: payload.ExpectedVersion, IdempotencyKey: payload.IdempotencyKey,
	})
	if err != nil {
		handoverWriteError(c, err)
		return
	}
	writeData(c, http.StatusOK, gin.H{"system": "project", "item_id": receipt.ItemID, "idempotency_key": receipt.IdempotencyKey,
		"status": receipt.Status, "applied_version": receipt.AppliedVersion, "reason_code": receipt.ReasonCode,
		"evidence_ref": receipt.EvidenceRef, "received_at": receipt.ReceivedAt})
}

func (h *Handler) handoverReceipt(c *gin.Context) {
	tenant, ok := handoverTenant(c, c.Query("tenant_id"))
	if !ok {
		return
	}
	receipt, err := h.handover.Receipt(c.Request.Context(), tenant, c.Query("idempotency_key"))
	if err != nil {
		handoverWriteError(c, err)
		return
	}
	writeData(c, http.StatusOK, gin.H{"system": "project", "item_id": receipt.ItemID, "idempotency_key": receipt.IdempotencyKey,
		"status": receipt.Status, "applied_version": receipt.AppliedVersion, "reason_code": receipt.ReasonCode,
		"evidence_ref": receipt.EvidenceRef, "received_at": receipt.ReceivedAt})
}

func (h *Handler) handoverComplete(c *gin.Context) {
	var payload handoverCompletePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		handoverWriteError(c, handover.ErrValidation)
		return
	}
	tenant, ok := handoverTenant(c, payload.TenantID)
	if !ok {
		return
	}
	if err := h.handover.Complete(c.Request.Context(), tenant, payload.BatchID, payload.Outcome); err != nil {
		handoverWriteError(c, err)
		return
	}
	writeData(c, http.StatusOK, gin.H{"system": "project", "batch_id": payload.BatchID, "outcome": strings.ToUpper(payload.Outcome)})
}

// handoverWriteError 把交接错误映射为确定性状态码，平台据此判断是否重试。
func handoverWriteError(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, handover.ErrValidation):
		writeError(c, http.StatusUnprocessableEntity, "PM_HANDOVER_VALIDATION", "交接请求参数不合法")
	case errors.Is(err, handover.ErrNotFound):
		writeError(c, http.StatusNotFound, "PM_HANDOVER_NOT_FOUND", "交接记录不存在")
	case errors.Is(err, handover.ErrFrozen):
		writeError(c, http.StatusConflict, "PM_HANDOVER_SUCCESSOR_FROZEN", "承接人处于交接冻结状态")
	case errors.Is(err, handover.ErrConflict):
		writeError(c, http.StatusConflict, "PM_HANDOVER_CONFLICT", "交接状态冲突")
	default:
		writeError(c, http.StatusInternalServerError, "PM_HANDOVER_INTERNAL", "交接处理失败")
	}
}

package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/oklog/ulid/v2"
)

// identitySyncAuditAction 是后台人员身份复核轮次汇总审计的动作前缀。
// prj_audit_outbox 没有扩展列，因此把租户数/检查数/状态变化数/失败数编码进
// action；平台对 action 的存储上限是 128 字符，这里的四个计数最多各 10 位，
// 加上固定前缀仍远低于上限（RecordIdentitySyncAudit 会再校验一次）。
const identitySyncAuditAction = "PROJECT_MANAGEMENT:IDENTITY_SYNC_COMPLETED"

// 后台通过窄接口类型断言接入；编译期断言防止方法签名漂移后静默降级为"未启用"。
var _ application.IdentitySyncStore = (*Repository)(nil)

// ListCapabilityTenants 枚举拥有人员资质档案的租户，供后台身份复核逐租户执行。
//
// 只读查询：SELECT DISTINCT tenant_id FROM pm_capability WHERE resource_type='PERSON'。
// 只有设备档案的租户没有可复核的人员身份，不进入结果；空表返回空切片而不是
// 错误，让后台把"还没有人员档案"当成一次正常的空轮次。结果按租户排序并去掉
// 空白项，保证轮次可重放且不会用空串发起一次没有租户归属的目录查询。
func (r *Repository) ListCapabilityTenants(ctx context.Context) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("identity sync repository is not configured")
	}
	var tenants []string
	if err := r.db.WithContext(ctx).Model(&capabilityRecord{}).
		Where("resource_type = ?", "PERSON").
		Distinct("tenant_id").
		Order("tenant_id").
		Pluck("tenant_id", &tenants).Error; err != nil {
		return nil, fmt.Errorf("list capability tenants: %w", err)
	}
	out := make([]string, 0, len(tenants))
	seen := make(map[string]struct{}, len(tenants))
	for _, tenant := range tenants {
		tenant = strings.TrimSpace(tenant)
		if tenant == "" {
			continue
		}
		if _, exists := seen[tenant]; exists {
			continue
		}
		seen[tenant] = struct{}{}
		out = append(out, tenant)
	}
	return out, nil
}

// RecordIdentitySyncAudit 写一条系统身份复核轮次的汇总审计（prj_audit_outbox）。
//
// 每轮只有一行，不逐人写：平台按 event_id 去重，投递失败由既有审计投递器
// 退避重试，不阻塞下一轮复核。审计属于后台进程自身，因此归属租户是部署租户
// （summary.TenantID），逐租户结果只体现在 action 里的计数与失败数上。
func (r *Repository) RecordIdentitySyncAudit(ctx context.Context, summary application.IdentitySyncSummary) error {
	if r == nil || r.db == nil {
		return errors.New("identity sync repository is not configured")
	}
	tenant := strings.TrimSpace(summary.TenantID)
	if tenant == "" || tenant != summary.TenantID || len(tenant) > 64 {
		return errors.New("identity sync audit tenant is invalid")
	}
	if summary.Tenants < 0 || summary.Checked < 0 || summary.Changed < 0 || summary.Failed < 0 {
		return errors.New("identity sync audit counts are invalid")
	}
	action := fmt.Sprintf("%s tenants=%d checked=%d changed=%d failed=%d", identitySyncAuditAction, summary.Tenants, summary.Checked, summary.Changed, summary.Failed)
	if len(action) > 128 {
		return errors.New("identity sync audit action exceeds platform limit")
	}
	result := "SUCCESS"
	if summary.Failed > 0 {
		// 部分租户失败时汇总审计按失败结论上报，计数里保留失败租户数；
		// 下一轮仍会重试全部租户，不能因一轮失败而静默降级。
		result = "FAILURE"
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Create(&auditOutboxRecord{
		ID: ulid.Make().String(), TenantID: tenant, EventID: ulid.Make().String(),
		ActorID: actorPointer(application.IdentitySyncSystemActor), ActorType: "SYSTEM",
		Action: action, ResourceType: "capability_identity_sync", ResourceID: ulid.Make().String(),
		IPSource: auditIPSource(""), Result: result,
		DeliveryStatus: "pending", NextAttemptAt: now, CreatedAt: now,
	}).Error
}

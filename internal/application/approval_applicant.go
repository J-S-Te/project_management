package application

import (
	"context"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// fillApprovalApplicantNames 补齐当前授权审批页的申请人姓名。
// 参数：ctx/p 为请求与已认证身份，rows 为仓储已授权的当前页。
// 返回值：无；目录缺失或无法核实时保留未知姓名。
func (s *Service) fillApprovalApplicantNames(ctx context.Context, p platform.Principal, rows []domain.ApprovalTask) {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.RequesterUserID
		if row.Type == domain.ApprovalNativeScope {
			ids[i] = row.RequesterDirectoryID
			if row.RequesterUserID == p.IdentityID {
				ids[i] = p.UserID
			}
		}
	}
	names := s.approvalApplicantNames(ctx, p, ids)
	for i, id := range ids {
		rows[i].RequesterDisplayName = names[strings.TrimSpace(id)]
	}
}

// fillNativeScopeApplicantNames 补齐已经授权读取的原生范围提案发起人姓名。
// 参数：ctx/p 为请求与已认证身份，changes 为授权读取的提案。
// 返回值：无；审批使用的原身份标识不参与姓名替换。
func (s *Service) fillNativeScopeApplicantNames(ctx context.Context, p platform.Principal, changes []domain.NativeScopeChange) {
	ids := make([]string, len(changes))
	for i, change := range changes {
		ids[i] = change.RequestedByUserID
		if change.RequestedBy == p.IdentityID {
			ids[i] = p.UserID
		}
	}
	names := s.approvalApplicantNames(ctx, p, ids)
	for i, id := range ids {
		changes[i].RequestedByDisplayName = names[strings.TrimSpace(id)]
	}
}

// approvalApplicantNames 仅解析调用方从可见申请中取到的账户，辅助目录失败时仍返回可用行。
// 参数：ctx/p 为请求和身份，ids 为来自有界列表的申请人平台用户号。
// 返回值：已精确核实的姓名映射；跳过机器、未知或失效账号。
func (s *Service) approvalApplicantNames(ctx context.Context, p platform.Principal, ids []string) map[string]string {
	var directoryIDs []string
	names := make(map[string]string)
	for i, id := range ids {
		ids[i] = strings.TrimSpace(id)
	}
	for _, id := range uniqueStrings(ids) {
		if id == "" || strings.EqualFold(id, "SYSTEM") {
			continue
		}
		if name := strings.TrimSpace(p.DisplayName); id == p.UserID && name != "" {
			names[id] = name
			continue
		}
		directoryIDs = append(directoryIDs, id)
	}
	// 姓名是辅助信息，避免目录网络异常长时间占用审批请求。
	directoryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for id, name := range s.lookupPersonnel(directoryCtx, directoryIDs).names {
		names[id] = name
	}
	return names
}

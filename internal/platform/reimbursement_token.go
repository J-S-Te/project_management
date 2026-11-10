package platform

import (
	"context"
	"fmt"
)

// NewReimbursementTokenVerifier 创建精确主体及单一用途的独立报销机器验签器。
// 参数：ctx 为初始化上下文，options 为完整信任绑定，仅允许规定的读取或支出scope。
// 返回值：不允许任意客户端主体的验签器；用途或配置不完整时返回错误。
func NewReimbursementTokenVerifier(ctx context.Context, options ClientCredentialsVerifierOptions) (ClientCredentialsTokenVerifier, error) {
	if options.CallerApplicationCode != "reimbursement" || options.AllowAnyClientSubject || (options.RequiredScope != "reimbursement.project_reference.read" && options.RequiredScope != "project.reimbursement_cost.write") {
		return nil, fmt.Errorf("%w: reimbursement trust purpose is invalid", ErrInvalidServiceToken)
	}
	return NewClientCredentialsTokenVerifier(ctx, options)
}

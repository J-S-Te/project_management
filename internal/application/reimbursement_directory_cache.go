package application

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type expenseDirectoryResult struct {
	page platform.OwnerDirectoryPage
	err  error
}
type expenseRequestDirectory struct {
	source    platform.OwnerDirectory
	expiresAt time.Time
	mu        sync.Mutex
	results   map[string]expenseDirectoryResult
}

// expenseDirectoryRequest 建立仅当前搜索或批验调用的目录视图，不修改共享Service或延长撤权窗口。
// 参数：无。
// 返回值：共享只读依赖但具有全新目录缓存的服务副本；没有目录时仍保留失败关闭语义。
func (s *Service) expenseDirectoryRequest() *Service {
	scoped := *s
	source := s.Personnel
	if cached, ok := source.(*expenseRequestDirectory); ok {
		source = cached.source
	}
	if source != nil {
		scoped.Personnel = &expenseRequestDirectory{source: source, expiresAt: time.Now().UTC().Add(2 * time.Minute), results: map[string]expenseDirectoryResult{}}
	}
	return &scoped
}

// List 按完整用户、角色、来源及分页参数复用本请求验证结果，下一请求必定重新读取。
// 参数：ctx为请求取消边界，query为真实目录过滤参数。
// 返回值：同批一致的页面或错误；请求窗口过期后不重新续期放行。
func (d *expenseRequestDirectory) List(ctx context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ctx.Err() != nil {
		return platform.OwnerDirectoryPage{}, ctx.Err()
	}
	if !time.Now().Before(d.expiresAt) {
		return platform.OwnerDirectoryPage{}, ErrServiceTimeout
	}
	raw, err := json.Marshal(query)
	if err != nil {
		return platform.OwnerDirectoryPage{}, ErrValidation
	}
	key := string(raw)
	if cached, exists := d.results[key]; exists {
		return cached.page, cached.err
	}
	page, err := d.source.List(ctx, query)
	if !time.Now().Before(d.expiresAt) {
		return platform.OwnerDirectoryPage{}, ErrServiceTimeout
	}
	d.results[key] = expenseDirectoryResult{page, err}
	return page, err
}

// expenseDirectoryLifetime 将每条引用窗口封顶到本请求目录快照窗口，防止多引用复用延长旧资格。
// 参数：reference为本次真实校验后的费用引用。
// 返回值：只可能缩短有效期的引用，不覆盖更早的显式资格到期。
func (s *Service) expenseDirectoryLifetime(reference domain.ExpenseProjectReference) domain.ExpenseProjectReference {
	if cached, ok := s.Personnel.(*expenseRequestDirectory); ok && cached.expiresAt.Before(reference.ValidUntil) {
		reference.ValidUntil = cached.expiresAt
	}
	return reference
}

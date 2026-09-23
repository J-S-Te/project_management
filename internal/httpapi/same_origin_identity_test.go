package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/j-s-te/project-management/internal/platform"
)

// pmOriginTestIdentity 返回零权限主体：同源校验通过后由路由层 require 给出 403，
// 与 PM_ORIGIN_REJECTED 形成可区分的断言，且全程不触碰业务 service。
type pmOriginTestIdentity struct{}

func (pmOriginTestIdentity) Authenticate(context.Context, *http.Request) (platform.Principal, error) {
	return platform.Principal{TenantID: "tenant-1", UserID: "user-1", DisplayName: "测试用户"}, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

package platform

import (
	"errors"
	"net/http"
	"net/url"

	central "github.com/j-s-te/project-management/internal/sessiongovernance"
)

// writeCentralLoginFailure 为旧中央周期执行一次新鲜登录，其余失败保留原分类。
// 参数：w/r为HTTP边界，tx为已消费事务，err为中央完成或上下文校验错误。
// 返回值：无；重登使用新state/nonce/PKCE，已强制登录过则停止，避免回调循环。
func (a *OIDCAuthenticator) writeCentralLoginFailure(w http.ResponseWriter, r *http.Request,
	tx LoginTransaction, err error) {
	if errors.Is(err, central.ErrInactive) && !tx.ForceLogin {
		query := url.Values{"prompt": {"login"}, "return_to": {safeReturnPath(tx.ReturnPath)}}
		http.Redirect(w, r, a.PublicPath("/auth/login")+"?"+query.Encode(), http.StatusSeeOther)
		return
	}
	status := http.StatusServiceUnavailable
	if errors.Is(err, central.ErrInactive) || errors.Is(err, central.ErrBindingConflict) {
		status = http.StatusUnauthorized
	}
	http.Error(w, "central session unavailable", status)
}

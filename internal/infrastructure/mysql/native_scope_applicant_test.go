package mysql

import (
	"context"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type nativeScopeDisplayDirectory struct{}

// List 提供与提案实际平台用户相符的单人目录资料。
// 参数：query 为按原提案已确认平台用户号发出的查询。
// 返回值：仅存在确切用户时返回姓名。
func (nativeScopeDisplayDirectory) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if query.UserID != "actual-user" {
		return platform.OwnerDirectoryPage{}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{
		{UserID: "actual-user", DisplayName: "业务申请人"},
	}}, nil
}

// TestNativeScopeDisplaysPersistedPlatformApplicant 验证提案的审批身份与目录用户号分别使用。
// 参数：t 为 SQLite 隔离测试上下文。
// 返回值：无；以审批人或发起人授权身份号代替真实申请人时失败。
func TestNativeScopeDisplaysPersistedPlatformApplicant(t *testing.T) {
	repository, project, items := scopeFixture(t)
	input := domain.NativeScopeInput{
		OperationID: "display-request", ExpectedProjectVersion: 1, BaseScopeVersion: 1,
		Reason: "确认范围", RequestedByUserID: "actual-user",
	}
	change, err := repository.RequestNativeScope(t.Context(), nativeScopeFilter(project), project.ID, "applicant-identity", input, items)
	if err != nil {
		t.Fatal(err)
	}
	p := platform.Principal{TenantID: project.TenantID, IdentityID: "reviewer-identity", UserID: "reviewer",
		DisplayName: "复核人", Permissions: map[string]bool{"project.read": true},
		DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	service := &application.Service{Repo: repository, NativeEnabled: true, Personnel: nativeScopeDisplayDirectory{}}
	page, pageErr := service.ReadNativeScope(t.Context(), p, project.ID)
	detail, detailErr := service.ReadNativeScopeChange(t.Context(), p, project.ID, change.ID)
	for _, read := range []struct {
		state domain.NativeScopeState
		err   error
	}{{page, pageErr}, {detail, detailErr}} {
		if read.err != nil || len(read.state.Changes) != 1 {
			t.Fatalf("范围提案读取失败：%+v %v", read.state, read.err)
		}
		actual := read.state.Changes[0]
		if actual.RequestedBy != "applicant-identity" || actual.RequestedByDisplayName != "业务申请人" {
			t.Fatalf("未使用持久的申请人平台账户：%+v", actual)
		}
	}
}

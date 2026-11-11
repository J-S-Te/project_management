package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// countingCapabilityRepository 在 capabilityRepository 之上统计身份回写次数，
// 并把回写结果同步进内存档案，模拟真实仓储"写回后下一次读取可见"的行为。
type countingCapabilityRepository struct {
	capabilityRepository
	updates      int
	lastStatuses map[string]string
}

func (r *countingCapabilityRepository) UpdateCapabilityIdentities(ctx context.Context, tenant string, statuses map[string]string, checkedAt time.Time) error {
	r.updates++
	r.lastStatuses = make(map[string]string, len(statuses))
	for userID, status := range statuses {
		r.lastStatuses[userID] = status
		for index := range r.capabilities {
			if r.capabilities[index].ResourceType == "PERSON" && r.capabilities[index].UserID == userID {
				r.capabilities[index].IdentityStatus = status
				r.capabilities[index].IdentityCheckedAt = checkedAt
			}
		}
	}
	return r.capabilityRepository.UpdateCapabilityIdentities(ctx, tenant, statuses, checkedAt)
}

// 后台每轮都会调用复核，目录结论与库内一致时不得重复写：同一结果重复跑
// 必须零写入，只有状态真正变化的那一轮才回写，并计入 Changed。
func TestSyncPersonnelIdentitiesDoesNotRewriteUnchangedStatuses(t *testing.T) {
	repo := &countingCapabilityRepository{capabilityRepository: capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "张三", UserID: "u-active", IdentityStatus: domain.IdentityStatusActive},
		{ResourceType: "PERSON", ResourceID: "P-002", ResourceName: "李四", UserID: "u-gone", IdentityStatus: domain.IdentityStatusMissing},
		{ResourceType: "PERSON", ResourceID: "P-003", ResourceName: "历史档案"},
	}}}
	service := &Service{Repo: repo, Personnel: directoryStub{names: map[string]string{"u-active": "张三"}}}
	principal := principalWith("project.resource.manage", platform.DataScope{RoleCode: "admin", ScopeType: "APPLICATION"})

	for round := 1; round <= 2; round++ {
		result, err := service.SyncPersonnelIdentities(context.Background(), principal)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if result.Changed != 0 || repo.updates != 0 {
			t.Fatalf("round %d rewrote an unchanged directory result: changed=%d updates=%d", round, result.Changed, repo.updates)
		}
		if result.Active != 1 || result.Missing != 1 || result.Unlinked != 1 || result.Total != 3 {
			t.Fatalf("round %d distribution = %+v", round, result)
		}
	}

	// 目录结论变化（这里模拟库内状态过期）时只回写真正变化的账号，且只写一次。
	repo.capabilities[1].IdentityStatus = domain.IdentityStatusActive
	result, err := service.SyncPersonnelIdentities(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed != 1 || repo.updates != 1 {
		t.Fatalf("stale status must be rewritten exactly once: changed=%d updates=%d", result.Changed, repo.updates)
	}
	if repo.lastStatuses["u-gone"] != domain.IdentityStatusMissing {
		t.Fatalf("written statuses = %+v, want u-gone=MISSING", repo.lastStatuses)
	}

	// 写回后同一结果再跑一轮必须重新回到零写入。
	if _, err = service.SyncPersonnelIdentities(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if repo.updates != 1 {
		t.Fatalf("unchanged rerun wrote again: updates=%d", repo.updates)
	}
}

// 系统维护路径不能"因为反正是后台"退化放行：缺权限码或缺全量数据范围都必须
// 拒绝，任何档案都不得被改写。这条约束也保护目录查询不被空租户主体触发。
func TestSyncPersonnelIdentitiesRejectsInsufficientPrincipal(t *testing.T) {
	for name, principal := range map[string]platform.Principal{
		"missing permission": principalWith("project.read", platform.DataScope{RoleCode: "viewer", ScopeType: "APPLICATION"}),
		"narrow scope":       principalWith("project.resource.manage", platform.DataScope{RoleCode: "pm", ScopeType: "PROJECT", ScopeID: "PJ-1"}),
		"empty principal":    {},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &countingCapabilityRepository{capabilityRepository: capabilityRepository{capabilities: []domain.Capability{
				{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "张三", UserID: "u-active", IdentityStatus: domain.IdentityStatusUnlinked},
			}}}
			service := &Service{Repo: repo, Personnel: directoryStub{names: map[string]string{"u-active": "张三"}}}
			if _, err := service.SyncPersonnelIdentities(context.Background(), principal); !errors.Is(err, ErrForbidden) {
				t.Fatalf("err = %v, want ErrForbidden", err)
			}
			if repo.updates != 0 || len(repo.identityStatuses) != 0 {
				t.Fatalf("unauthorized sync wrote identities: updates=%d statuses=%+v", repo.updates, repo.identityStatuses)
			}
			if repo.capabilities[0].IdentityStatus != domain.IdentityStatusUnlinked {
				t.Fatalf("unauthorized sync mutated the record: %+v", repo.capabilities[0])
			}
		})
	}
}

package application

import (
	"context"
	"errors"
	"testing"
)

type freezeCheckerStub struct {
	frozen bool
	err    error
	calls  int
}

func (stub *freezeCheckerStub) ActiveFreezeUser(_ context.Context, _, _ string) (bool, error) {
	stub.calls++
	return stub.frozen, stub.err
}

// 交接冻结中的用户不得承接新项目责任。
func TestEnsureManagerNotFrozenRejectsFrozenManager(t *testing.T) {
	checker := &freezeCheckerStub{frozen: true}
	service := &Service{}
	service.UseFreezeChecker(checker)
	if err := service.ensureManagerNotFrozen(context.Background(), "tenant-1", "user-leaver"); !errors.Is(err, ErrManagerFrozenByHandover) {
		t.Fatalf("err = %v, want ErrManagerFrozenByHandover", err)
	}
	if checker.calls != 1 {
		t.Fatalf("freeze lookups = %d, want 1", checker.calls)
	}
}

// 未冻结放行；未接线或空经理不查询（向后兼容）。
func TestEnsureManagerNotFrozenAllowsAndSkips(t *testing.T) {
	service := &Service{}
	service.UseFreezeChecker(&freezeCheckerStub{frozen: false})
	if err := service.ensureManagerNotFrozen(context.Background(), "tenant-1", "user-1"); err != nil {
		t.Fatalf("unfrozen manager must be accepted: %v", err)
	}
	unwired := &Service{}
	if err := unwired.ensureManagerNotFrozen(context.Background(), "tenant-1", "user-1"); err != nil {
		t.Fatalf("unwired checker must not block: %v", err)
	}
	checker := &freezeCheckerStub{frozen: true}
	wired := &Service{}
	wired.UseFreezeChecker(checker)
	if err := wired.ensureManagerNotFrozen(context.Background(), "tenant-1", "  "); err != nil {
		t.Fatalf("empty manager must be skipped: %v", err)
	}
	if checker.calls != 0 {
		t.Fatalf("empty manager must not trigger a lookup, calls = %d", checker.calls)
	}
}

// 查询失败必须 失败时安全拒绝 上报，不能当作未冻结放行。
func TestEnsureManagerNotFrozenReportsLookupFailure(t *testing.T) {
	service := &Service{}
	service.UseFreezeChecker(&freezeCheckerStub{err: errors.New("freeze store unavailable")})
	err := service.ensureManagerNotFrozen(context.Background(), "tenant-1", "user-1")
	if err == nil || errors.Is(err, ErrManagerFrozenByHandover) {
		t.Fatalf("err = %v, want a lookup failure", err)
	}
}

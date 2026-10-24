package application

import (
	"context"
	"errors"
	"testing"
)

type personnelFreezeStub struct{ frozen map[string]bool }

func (stub personnelFreezeStub) ActiveFreezeUser(_ context.Context, _, userID string) (bool, error) {
	return stub.frozen[userID], nil
}

// 团队负责人、项目经理、工程师都会承接实际实施责任：
// 任一处于交接冻结时应拒绝分配，与创建项目同一口径。
func TestEnsurePersonnelNotFrozenCoversAllResponsibilityTypes(t *testing.T) {
	service := (&Service{}).UseFreezeChecker(personnelFreezeStub{frozen: map[string]bool{"user-leaver": true}})

	if err := service.ensurePersonnelNotFrozen(context.Background(), "tenant-1", "user-ok", "user-leaver"); !errors.Is(err, ErrManagerFrozenByHandover) {
		t.Fatalf("frozen engineer must be rejected, got %v", err)
	}
	if err := service.ensurePersonnelNotFrozen(context.Background(), "tenant-1", "user-ok", "user-ok2"); err != nil {
		t.Fatalf("all clear must pass, got %v", err)
	}
	if err := service.ensurePersonnelNotFrozen(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("empty personnel must pass, got %v", err)
	}
	unwired := &Service{}
	if err := unwired.ensurePersonnelNotFrozen(context.Background(), "tenant-1", "user-leaver"); err != nil {
		t.Fatalf("unwired checker must keep legacy behavior, got %v", err)
	}
}

package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	drivermysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type effectsIntegrationDirectory struct{ fail bool }

func (d *effectsIntegrationDirectory) List(_ context.Context, q platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if d.fail && q.Page == 2 {
		return platform.OwnerDirectoryPage{}, errors.New("directory offline")
	}
	rows := []platform.OwnerDirectoryUser{}
	for i := (q.Page - 1) * q.PageSize; i < min(q.Page*q.PageSize, 51); i++ {
		rows = append(rows, platform.OwnerDirectoryUser{UserID: fmt.Sprintf("u-%02d", i)})
	}
	return platform.OwnerDirectoryPage{Items: rows, Page: q.Page, PageSize: q.PageSize, Total: 51}, nil
}

func TestDeliveryEffectsTransactionRecoveryAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set dedicated PM_TEST_DSN to run delivery-effects integration checks")
	}
	ctx := context.Background()
	if err := migration.Run(ctx, dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(drivermysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	tenant := ulid.Make().String()
	t.Cleanup(func() {
		for _, table := range []string{"prj_audit_outbox", "pm_notification_outbox", "pm_delivery_effects_outbox", "pm_delivery_event", "pm_automation", "pm_project"} {
			if err := db.Exec("DELETE FROM "+table+" WHERE tenant_id = ?", tenant).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	now := time.Now().UTC().Truncate(time.Millisecond)
	automation := automationRecord{TenantID: tenant, Kind: "automations", Name: "通知技术总监", Trigger: application.EventContractActivated, Target: "technical_director", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&automation).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	project := domain.Project{ID: "PJ-" + ulid.Make().String(), TenantID: tenant, Name: "effects regression", Contract: "CT-effects", ContractID: "CT-effects", ContractVersion: "v1", Status: "待确认", SupplementStatus: "NONE", CreatedAt: now, UpdatedAt: now}
	event := domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: tenant, ProjectID: project.ID, Type: application.EventContractActivated, CreatedAt: now, Payload: map[string]any{"contract_id": project.ContractID}, RoleNotification: &domain.RoleNotification{Roles: []string{"business_admin"}, Message: domain.NotificationMessage{EventID: ulid.Make().String(), EventType: "SPLIT_RULE_MISSING", Scope: platform.NotificationScopeCrossSystem, Priority: "HIGH", Title: "规则缺失", Content: "请确认", ReferenceType: "project", ReferenceID: project.ID, OccurredAt: now, IdempotencyKey: project.ID + "-split"}}}
	if err := repo.ActivateContract(ctx, project, nil, event); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&deliveryEffectsRecord{}).Where("tenant_id = ?", tenant).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("source/effects commit=%d/%v", count, err)
	}
	var stored deliveryEffectsRecord
	if err := db.Where("tenant_id = ?", tenant).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	// 重复意图使整个生产事务失败，而不仅是通知失败。
	rollbackProject := project
	rollbackProject.ID = "PJ-" + ulid.Make().String()
	rollbackProject.ContractID = "CT-rollback"
	rollbackProject.Contract = "CT-rollback"
	rollbackEvent := event
	rollbackEvent.ProjectID = rollbackProject.ID
	if err := repo.ActivateContract(ctx, rollbackProject, nil, rollbackEvent); err == nil {
		t.Fatal("duplicate source identity accepted")
	}
	if err := db.Model(&projectRecord{}).Where("tenant_id = ? AND id = ?", tenant, rollbackProject.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed event left project committed")
	}

	claimed, err := repo.ClaimDeliveryEffects(ctx, tenant, "worker-a", 1, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%v/%v", claimed, err)
	}
	if again, err := repo.ClaimDeliveryEffects(ctx, tenant, "worker-b", 1, now.Add(time.Minute)); err != nil || len(again) != 0 {
		t.Fatal("live lease stolen")
	}
	afterRestart := NewRepository(db)
	recovered, err := afterRestart.ClaimDeliveryEffects(ctx, tenant, "worker-b", 1, now.Add(3*time.Minute))
	if err != nil || len(recovered) != 1 || recovered[0].Effects.ID != claimed[0].Effects.ID {
		t.Fatal("restart changed or lost intent identity")
	}
	if err := repo.CompleteDeliveryEffects(ctx, tenant, stored.ID, "worker-a", nil, nil, now.Add(3*time.Minute)); !errors.Is(err, application.ErrConflict) {
		t.Fatal("stale lease owner can complete")
	}
	if err := afterRestart.RetryDeliveryEffects(ctx, tenant, stored.ID, "worker-b", now.Add(3*time.Minute), now.Add(3*time.Minute), "RESTART_RECOVERY"); err != nil {
		t.Fatal(err)
	}
	// 规则编辑不能改变已接受事件的目标。
	if err := db.Model(&automation).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	directory := &effectsIntegrationDirectory{fail: true}
	service := &application.Service{Repo: afterRestart, Personnel: directory}
	if completed, err := service.DispatchDeliveryEffects(ctx, tenant, "worker-c", 5, now.Add(3*time.Minute)); err != nil || completed != 0 {
		t.Fatalf("failed lookup=%d/%v", completed, err)
	}
	if err := db.Model(&notificationOutboxRecord{}).Where("tenant_id = ?", tenant).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("partial directory wrote notifications")
	}
	directory.fail = false
	if completed, err := service.DispatchDeliveryEffects(ctx, tenant, "worker-d", 5, now.Add(10*time.Minute)); err != nil || completed != 1 {
		t.Fatalf("recovered=%d/%v", completed, err)
	}
	if err := db.Model(&notificationOutboxRecord{}).Where("tenant_id = ?", tenant).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("two durable messages expected=%d/%v", count, err)
	}
	if err := db.Model(&deliveryEventRecord{}).Where("tenant_id = ?", tenant).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("one source + one derived expected=%d/%v", count, err)
	}
	if completed, err := service.DispatchDeliveryEffects(ctx, tenant, "worker-e", 5, now.Add(11*time.Minute)); err != nil || completed != 0 {
		t.Fatal("completed effects replayed")
	}
	if err := db.Where("id = ?", stored.ID).Take(&stored).Error; err != nil || stored.Status != "COMPLETED" || stored.RetryCount != 2 {
		t.Fatalf("job recovery state=%+v/%v", stored, err)
	}
}

package application

import (
	"context"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type deviationNoticeRepository struct{ hookRepository }

// GetServiceItem 返回当前租户合成服务项的原通知参与者。
// 参数：ctx/filter/id 为真实通知查询传入的上下文、租户及服务项。
// 返回值：只有合成项目的负责人；不访问外部服务。
func (r *deviationNoticeRepository) GetServiceItem(ctx context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	return domain.ServiceItem{ID: id, TenantID: filter.TenantID, ProjectID: "project",
		TeamLeadID: "lead-user", ProjectManagerID: "manager-user"}, nil
}

// TestDeviationNotificationKeepsAudienceAndPreciseInstance 验证偏离提醒保留原受众并直达实例。
// 参数：t 为测试上下文，仅替换持久化接口边界。
// 返回值：无；旧实例坐标、受众变化或敏感说明进入提醒时失败。
func TestDeviationNotificationKeepsAudienceAndPreciseInstance(t *testing.T) {
	repo := &deviationNoticeRepository{}
	service := Service{Repo: repo}
	event := domain.DeliveryEvent{ID: "event", TenantID: "tenant", ProjectID: "project",
		ServiceItemID: "item", Type: EventDeviationReported, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{"deviation_id": "deviation/1", "description": "内部敏感说明"}}
	if err := service.applyEvent(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	message := repo.events[0].Notification
	if message == nil {
		t.Fatal("missing default deviation notification")
	}
	target, _ := url.Parse(message.TargetURL)
	if message.ReferenceType != "DEVIATION" || message.ReferenceID != "deviation/1" ||
		target.Path != "/project_management/exceptions" ||
		target.Query().Get("service_item_id") != "item" || target.Query().Get("deviation_id") != "deviation/1" {
		t.Fatalf("notification=%+v", message)
	}
	if !reflect.DeepEqual(message.Recipients, []string{"lead-user", "manager-user"}) || message.Content == "内部敏感说明" {
		t.Fatalf("audience/content=%+v", message)
	}
}

// TestNativeScopeNotificationUsesExistingRoles 验证原生提醒只解析既有技术总监和项目管理员。
// 参数：t 为测试上下文，角色目录仅返回平台用户编号。
// 返回值：无；遗漏管理员、重复受众或把申请者作为审批人时失败。
func TestNativeScopeNotificationUsesExistingRoles(t *testing.T) {
	service := Service{Personnel: roleDirectoryStub{byRole: map[string][]string{
		"technical_director": {"user-director", "user-applicant"},
		"admin":              {"user-admin", "user-director"},
	}}}
	users, err := service.resolveApprovalRecipients(t.Context(), domain.NotificationMessage{
		EventType: "NATIVE_SCOPE_CHANGE_REQUESTED", RecipientRole: "technical_director,admin",
		ExcludeUserID: "user-applicant",
	})
	if err != nil || !reflect.DeepEqual(users, []string{"user-director", "user-admin"}) {
		t.Fatalf("users=%v err=%v", users, err)
	}
}

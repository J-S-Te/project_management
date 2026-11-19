package application

import (
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestAttachApprovalNotificationCoversRequestAndDecision 锁定审批链三类提醒：
// 申请待审批给技术总监，通过/驳回给申请人，目标统一指向申请详情。
func TestAttachApprovalNotificationCoversRequestAndDecision(t *testing.T) {
	cases := []struct {
		name        string
		eventType   string
		payload     map[string]any
		wantRole    string
		wantRecip   string
		wantTitle   string
		wantRequest string
	}{
		{"现场回退待审批", EventRollbackRequested, map[string]any{"kind": "FIELD_TO_PREPARE", "rollback_requester_id": "u-req"}, "technical_director", "", "现场回退待审批", "evt-1"},
		{"报告返工待审批", EventRollbackRequested, map[string]any{"kind": "REPORT_TO_FIELD", "rollback_requester_id": "u-req"}, "technical_director", "", "报告返工待审批", "evt-1"},
		{"报告更正待审批", EventReportCorrectionRequested, map[string]any{"reason": "版本更正"}, "technical_director", "", "报告更正待审批", "evt-1"},
		{"回退已批准", EventRollbackApproved, map[string]any{"request_id": "req-1", "rollback_requester_id": "u-req"}, "", "u-req", "现场回退申请已通过", "req-1"},
		{"回退已驳回", EventRollbackRejected, map[string]any{"request_id": "req-2", "rollback_requester_id": "u-req"}, "", "u-req", "现场回退申请已驳回", "req-2"},
		{"更正已批准", EventReportCorrectionApproved, map[string]any{"request_id": "req-3", "report_correction_requester_id": "u-req2"}, "", "u-req2", "报告更正申请已通过", "req-3"},
		{"更正已驳回", EventReportCorrectionRejected, map[string]any{"request_id": "req-4", "report_correction_requester_id": "u-req2"}, "", "u-req2", "报告更正申请已驳回", "req-4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := &domain.DeliveryEvent{ID: "evt-1", TenantID: "tenant-1", ProjectID: "project-1",
				ServiceItemID: "item-1", ActorUserID: "u-actor", Type: tc.eventType, CreatedAt: time.Now().UTC(), Payload: tc.payload}
			if !AttachApprovalNotification(event) {
				t.Fatal("approval event must attach a notification")
			}
			message := event.Notification
			if message == nil {
				t.Fatal("notification is nil")
			}
			if message.RecipientRole != tc.wantRole {
				t.Fatalf("recipient role = %q, want %q", message.RecipientRole, tc.wantRole)
			}
			if tc.wantRecip != "" {
				if len(message.Recipients) != 1 || message.Recipients[0] != tc.wantRecip {
					t.Fatalf("recipients = %#v, want %q", message.Recipients, tc.wantRecip)
				}
			}
			if message.Title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", message.Title, tc.wantTitle)
			}
			wantTarget := "/project_management/approvals?request_id=" + tc.wantRequest
			if message.TargetURL != wantTarget {
				t.Fatalf("target = %q, want %q", message.TargetURL, wantTarget)
			}
			if message.ReferenceType != "project_approval" || message.ReferenceID != tc.wantRequest {
				t.Fatalf("reference = %q/%q, want project_approval/%q", message.ReferenceType, message.ReferenceID, tc.wantRequest)
			}
		})
	}
}

// TestAttachApprovalNotificationIgnoresOrdinaryDeliveryEvents 断言普通交付事件不会被误当成审批提醒。
func TestAttachApprovalNotificationIgnoresOrdinaryDeliveryEvents(t *testing.T) {
	event := &domain.DeliveryEvent{ID: "evt-1", ServiceItemID: "item-1", Type: EventImplementationPlanned, Payload: map[string]any{}}
	if AttachApprovalNotification(event) {
		t.Fatal("ordinary delivery event must not attach approval notification")
	}
	if event.Notification != nil {
		t.Fatal("ordinary delivery event must not get a notification")
	}
}

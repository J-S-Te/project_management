package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Stored status/count/progress must never override the complete active item set.
// 报告阶段必须与状态同源：同一批未归档/未终止服务项、同一派生函数，空串表示
// 未进入报告段或不可判定，且投影始终输出 report_stage 键（"report_stage":""）。
func TestProjectProjectionMatchesAuthoritativeDomain(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 123, time.FixedZone("CST", 8*3600))
	archived := now.Add(time.Hour)
	field := func(report string) ProjectSnapshotItem {
		return ProjectSnapshotItem{Status: ProjectStatusFieldCompleted, ReportStatus: report}
	}
	for _, tc := range []struct {
		name, supplement, stored string
		items                    []ProjectSnapshotItem
		status, stage            string
		progress, count          int
		risk, flight             bool
	}{
		{"completed with termination", "", "实施中", []ProjectSnapshotItem{field("ARCHIVED"), {Status: ProjectStatusTerminated}}, ProjectStatusCompleted, "ARCHIVED", 100, 2, true, false},
		{"archive excluded", "", "实施中", []ProjectSnapshotItem{field("ARCHIVED"), {Status: ProjectStatusException, ArchivedAt: &archived}}, ProjectStatusCompleted, "ARCHIVED", 100, 1, false, false},
		{"supplement takes priority", "REQUIRED", "", []ProjectSnapshotItem{field("ARCHIVED")}, ProjectStatusSupplementRequired, "ARCHIVED", 100, 1, false, true},
		{"empty project", "", "待实施", nil, ProjectStatusPendingExecution, "", 0, 0, false, true},
		{"unknown is conservative", "", "已完成", []ProjectSnapshotItem{{Status: "UNKNOWN"}}, ProjectStatusPendingDecomposition, "", 0, 1, false, true},
		{"terminated", "", "", []ProjectSnapshotItem{{Status: ProjectStatusTerminated}}, ProjectStatusTerminated, "", 0, 1, true, true},
		{"rectifying", "", "", []ProjectSnapshotItem{field("RECTIFYING")}, ProjectStatusReporting, "RECTIFYING", 87, 1, false, true},
		{"mixed takes the lagging stage", "", "", []ProjectSnapshotItem{field("NONE"), field("RECTIFYING")}, ProjectStatusFieldCompleted, "NONE", 81, 2, false, true},
		{"terminated item does not join the report stage", "", "", []ProjectSnapshotItem{{Status: ProjectStatusTerminated, ReportStatus: "ARCHIVED"}}, ProjectStatusTerminated, "", 0, 1, true, true},
		{"unknown report code is undecidable", "", "", []ProjectSnapshotItem{field("DRAFTING")}, ProjectStatusFieldCompleted, "", 75, 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjectProjection(Project{TenantID: "tenant", ID: "P-1", Name: "中文项目", Customer: "客户", CustomerID: "C-1", Contract: "合同", ContractID: "HT-1", Category: "类别", Team: "展示团队", Manager: "经理", ManagerIdentityID: "manager-id", OwnerOrgID: "org-id", OwnerIdentityID: "owner-id", Services: 999, Progress: 98, Status: tc.stored, SupplementStatus: tc.supplement, CreatedAt: now, UpdatedAt: now}, "snapshot", tc.items, now)
			if p.Type != "project" || p.Status != tc.status || p.ReportStage != tc.stage || p.Progress != tc.progress || p.ServiceItems != tc.count || p.Risk != tc.risk || p.InFlight != tc.flight {
				t.Fatalf("projection=%+v", p)
			}
			for key, pair := range map[string][2]string{"customer": {p.Customer, "客户"}, "customer_id": {p.CustomerID, "C-1"}, "contract": {p.Contract, "合同"}, "contract_id": {p.ContractID, "HT-1"}, "category": {p.Category, "类别"}, "team": {p.Team, "展示团队"}, "manager": {p.Manager, "经理"}, "manager_identity_id": {p.ManagerIdentityID, "manager-id"}, "owner_org_id": {p.OwnerOrgID, "org-id"}, "owner_identity_id": {p.OwnerIdentityID, "owner-id"}} {
				if pair[0] != pair[1] {
					t.Fatalf("%s=%q want %q", key, pair[0], pair[1])
				}
			}
			line, err := ProjectSnapshotLine(p)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err = json.Unmarshal(line, &fields); err != nil {
				t.Fatal(err)
			}
			wireStage, present := fields["report_stage"]
			if len(fields) != 24 || !present || wireStage != tc.stage || fields["created_at"] != "2026-10-23T02:00:00.000000123Z" || !strings.Contains(string(line), "中文项目") {
				t.Fatalf("wire fields=%s", line)
			}
			// 新 PM 始终输出该键：空阶段是 "" 而不是缺失、不是 null。
			if !strings.Contains(string(line), `"report_stage":"`+tc.stage+`"`) {
				t.Fatalf("wire report_stage=%s", line)
			}
		})
	}
}

func TestProjectSnapshotLineRejectsInvalidUTF8(t *testing.T) {
	for name, projection := range map[string]ProjectProjection{
		"name":         {Type: "project", Name: string([]byte{0xff})},
		"report_stage": {Type: "project", ReportStage: string([]byte{0xff})},
	} {
		if _, err := ProjectSnapshotLine(projection); err == nil {
			t.Fatalf("%s: invalid UTF-8 must not silently become replacement characters", name)
		}
	}
}

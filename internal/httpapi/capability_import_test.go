package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

func TestCapabilitiesFromImportCSVByColumnName(t *testing.T) {
	rows, errs := capabilitiesFromImportCSV([][]string{
		{"resource_name", "codes", "resource_type", "resource_id", "status", "valid_until", "valid_from"},
		{"张三", "低压电工证，登高证；特种", "person", "P-001", "active", "2026-12-31", "2026-01-01"},
		{"基站A", "巡检 月度", "equipment", "E-001", "", "", ""},
	})
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	first := rows[0]
	if first.ResourceType != "PERSON" || first.ResourceID != "P-001" || first.ResourceName != "张三" || first.Status != "ACTIVE" {
		t.Fatalf("first=%+v", first)
	}
	if strings.Join(first.Codes, "|") != "低压电工证|登高证|特种" {
		t.Fatalf("codes=%v", first.Codes)
	}
	if !first.ValidFrom.IsZero() || !first.ValidUntil.IsZero() {
		t.Fatalf("personnel validity dates must be ignored: %v~%v", first.ValidFrom, first.ValidUntil)
	}
	if second := rows[1]; second.ResourceType != "EQUIPMENT" || second.Status != "ACTIVE" || !second.ValidFrom.IsZero() || !second.ValidUntil.IsZero() {
		t.Fatalf("second=%+v", second)
	}
}

func TestCapabilitiesFromImportCSVByFixedColumnOrder(t *testing.T) {
	rows, errs := capabilitiesFromImportCSV([][]string{
		{"PERSON", "P-001", "张三", "低压电工证;登高证", "", "", ""},
		{"EQUIPMENT", "E-001", "基站A", "巡检", "ACTIVE", "2026-01-01T08:00:00Z", "2026-12-31 23:59:59"},
	})
	if len(errs) != 0 || len(rows) != 2 {
		t.Fatalf("rows=%d errs=%v", len(rows), errs)
	}
	if rows[0].Status != "ACTIVE" || !rows[0].ValidFrom.IsZero() {
		t.Fatalf("row0=%+v", rows[0])
	}
	if rows[1].Status != "ACTIVE" || rows[1].ValidFrom.Format("2006-01-02") != "2026-01-01" || rows[1].ValidUntil.Format("2006-01-02") != "2026-12-31" {
		t.Fatalf("row1=%+v", rows[1])
	}
}

func TestCapabilitiesFromImportCSVReportsMalformedLines(t *testing.T) {
	rows, errs := capabilitiesFromImportCSV([][]string{
		{"resource_type", "resource_id", "resource_name", "codes", "valid_from", "valid_until"},
		{"PERSON", "P-001", "张三", "低压电工证", "不是日期", ""},
		{"EQUIPMENT", "E-001", "设备", "巡检", "2026-01-01", "乱写的日期"},
	})
	if len(rows) != 1 || len(errs) != 1 {
		t.Fatalf("rows=%d errs=%v", len(rows), errs)
	}
	if rows[0].ResourceType != "PERSON" || !rows[0].ValidFrom.IsZero() || !rows[0].ValidUntil.IsZero() {
		t.Fatalf("personnel row=%+v", rows[0])
	}
	if !strings.HasPrefix(errs[0], "csv 第 3 行") {
		t.Fatalf("errs=%v", errs)
	}
}

// 能力/设备导出必须按 SEC-B6 口径转义公式注入载荷：=、+、-、@ 前缀与前导 tab/CR
// 一律加 ' 前缀；普通文本保持原样（既有口径没有纯数字豁免，-1 同样按 '-' 前缀处理）。
func TestEscapeCSVFormulaCellNeutralizesFormulaPayloads(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "等号公式", input: "=cmd|'/c calc'!A0", want: "'=cmd|'/c calc'!A0"},
		{name: "加号公式", input: "+SUM(1)", want: "'+SUM(1)"},
		{name: "AT符号公式", input: "@x", want: "'@x"},
		{name: "减号字面值", input: "-1", want: "'-1"},
		{name: "前导tab公式", input: "\t=1+1", want: "'\t=1+1"},
		{name: "前导CR公式", input: "\r=1+1", want: "'\r=1+1"},
		{name: "普通文本不动", input: "张三-高压电工证", want: "张三-高压电工证"},
		{name: "空串不动", input: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := escapeCSVFormulaCell(test.input); got != test.want {
				t.Fatalf("escapeCSVFormulaCell(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

// 导出行的每个用户可控单元格都要转义，且列顺序、列数保持不变。
func TestCapabilityExportRowEscapesUserControlledCells(t *testing.T) {
	row := capabilityExportRow(domain.Capability{
		ResourceType: "PERSON",
		ResourceID:   "=cmd|'/c calc'!A0",
		ResourceName: "@SUM(A1)",
		Codes:        []string{"+SUM(1)", "低压电工证"},
		Status:       "ACTIVE",
	})
	if len(row) != len(capabilityImportColumns) {
		t.Fatalf("列数=%d, want %d", len(row), len(capabilityImportColumns))
	}
	if row[1] != "'=cmd|'/c calc'!A0" {
		t.Fatalf("resource_id 未转义: %q", row[1])
	}
	if row[2] != "'@SUM(A1)" {
		t.Fatalf("resource_name 未转义: %q", row[2])
	}
	if row[3] != "'+SUM(1);低压电工证" {
		t.Fatalf("codes 未转义: %q", row[3])
	}
	if row[0] != "PERSON" || row[4] != "ACTIVE" || row[5] != "" || row[6] != "" {
		t.Fatalf("普通单元格被改写: %v", row)
	}

	equipment := capabilityExportRow(domain.Capability{
		ResourceType: "EQUIPMENT", ResourceID: "E-1", ResourceName: "基站A", Codes: []string{"巡检"},
		Status: "ACTIVE", ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidUntil: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
	})
	if equipment[5] != "2026-01-01" || equipment[6] != "2026-12-31" {
		t.Fatalf("设备有效期被改写: %v", equipment)
	}
}

package httpapi

import (
	"strings"
	"testing"
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
	if first.ValidFrom.Format("2006-01-02") != "2026-01-01" || first.ValidUntil.Format("2006-01-02") != "2026-12-31" {
		t.Fatalf("dates=%v~%v", first.ValidFrom, first.ValidUntil)
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
		{"PERSON", "P-001", "张三", "低压电工证", "2026-01-01", "乱写的日期"},
	})
	if len(rows) != 0 || len(errs) != 2 {
		t.Fatalf("rows=%d errs=%v", len(rows), errs)
	}
	if !strings.HasPrefix(errs[0], "csv 第 2 行") || !strings.HasPrefix(errs[1], "csv 第 3 行") {
		t.Fatalf("errs=%v", errs)
	}
}
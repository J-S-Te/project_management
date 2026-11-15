package migrations

import (
	"strings"
	"testing"
)

// TestMetricCalendarAutomationMigrationAddsCalendarExceptionsAndSourceBoundaries 固定 P2/P3 的结构边界。
func TestMetricCalendarAutomationMigrationAddsCalendarExceptionsAndSourceBoundaries(t *testing.T) {
	body, err := Files.ReadFile("000048_metric_calendar_automation.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS pm_metric_work_calendar",
		"CREATE TABLE IF NOT EXISTS pm_metric_calendar_exception",
		"CREATE TABLE IF NOT EXISTS pm_metric_auto_confirm_setting",
		"uq_pm_metric_calendar_weekday",
		"uq_pm_metric_exception_day",
		"ADD COLUMN source VARCHAR(32) NOT NULL DEFAULT ''MANUAL''",
		"ADD COLUMN source_ref VARCHAR(128) NOT NULL DEFAULT ''''",
		"ADD COLUMN source_event_ids JSON NULL",
		"ADD COLUMN auto_confirmed TINYINT(1) NOT NULL DEFAULT 0",
		"GENERATED ALWAYS AS (NULLIF(source_ref,'''')) STORED",
		"UNIQUE INDEX uq_pm_usage_source_ref(tenant_id,source_ref_key)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("metric automation migration missing %q", required)
		}
	}
	if strings.Contains(sql, "UPDATE pm_resource_usage") || strings.Contains(sql, "DELETE FROM") {
		t.Fatal("migration must not rewrite historical usage")
	}
}

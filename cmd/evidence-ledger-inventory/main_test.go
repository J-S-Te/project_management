package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// D1 只读盘点命令的纯函数测试：不连接任何数据库，只覆盖参数解析、
// 连接配置解析、只读 SQL 形状、样本 ID 解析与输出脱敏。

func TestParseArgsDefaultsToDryRunTable(t *testing.T) {
	opts, err := parseArgs(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.format != "table" {
		t.Fatalf("format = %q, want table", opts.format)
	}
	if opts.tenant != "" {
		t.Fatalf("tenant = %q, want empty (all tenants)", opts.tenant)
	}
	if opts.timeout != defaultTimeout {
		t.Fatalf("timeout = %s, want %s", opts.timeout, defaultTimeout)
	}
}

func TestParseArgsAcceptsJSONAndTenant(t *testing.T) {
	opts, err := parseArgs([]string{"--format", " JSON ", "--tenant", " tenant-a ", "--timeout", "5s"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.format != "json" {
		t.Fatalf("format = %q, want json", opts.format)
	}
	if opts.tenant != "tenant-a" {
		t.Fatalf("tenant = %q, want tenant-a", opts.tenant)
	}
	if opts.timeout != 5*time.Second {
		t.Fatalf("timeout = %s, want 5s", opts.timeout)
	}
}

func TestParseArgsRejectsApplyInsteadOfImplementingIt(t *testing.T) {
	_, err := parseArgs([]string{"--apply"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseArgs(--apply) error = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "需单独立项") {
		t.Fatalf("apply refusal message = %q, want it to mention 需单独立项", err.Error())
	}
}

func TestParseArgsRejectsUnsupportedFormat(t *testing.T) {
	_, err := parseArgs([]string{"--format", "yaml"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseArgs(--format yaml) error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "table 或 json") {
		t.Fatalf("format error = %q, want supported formats hint", err.Error())
	}
}

func TestParseArgsRejectsPositionalArgumentsAndBadTimeout(t *testing.T) {
	if _, err := parseArgs([]string{"extra"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "位置参数") {
		t.Fatalf("positional argument error = %v, want positional rejection", err)
	}
	if _, err := parseArgs([]string{"--timeout", "0s"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "大于 0") {
		t.Fatalf("zero timeout error = %v, want positive timeout rejection", err)
	}
}

func TestValidateTenantFilter(t *testing.T) {
	for _, tenant := range []string{"", "tenant-01", "TENANT_01.a"} {
		if err := validateTenantFilter(tenant); err != nil {
			t.Fatalf("validateTenantFilter(%q) error = %v", tenant, err)
		}
	}
	for _, tenant := range []string{strings.Repeat("a", 65), "tenant 01", "tenant\n01"} {
		if err := validateTenantFilter(tenant); err == nil {
			t.Fatalf("validateTenantFilter(%q) error = nil, want rejection", tenant)
		}
	}
}

func TestResolveDSNRequiresSameEnvVarAsExistingCommands(t *testing.T) {
	if _, err := resolveDSN(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "MYSQL_DSN") {
		t.Fatalf("resolveDSN(empty) error = %v, want MYSQL_DSN requirement", err)
	}
	if _, err := resolveDSN(func(string) string { return "   " }); err == nil {
		t.Fatal("resolveDSN(whitespace) error = nil, want rejection")
	}
	dsn := "user:secret@tcp(127.0.0.1:3306)/project_management"
	got, err := resolveDSN(func(key string) string {
		if key != "MYSQL_DSN" {
			t.Fatalf("unexpected env key %q", key)
		}
		return "  " + dsn + "  "
	})
	if err != nil {
		t.Fatalf("resolveDSN() error = %v", err)
	}
	if got != dsn {
		t.Fatalf("resolveDSN() = %q, want trimmed DSN", got)
	}
}

// forbiddenSQL 是盘点命令绝不允许出现的写语句；只读命令必须保持 SELECT-only。
var forbiddenSQL = []string{
	"INSERT ", "UPDATE ", "DELETE ", "REPLACE INTO", "ALTER ", "DROP ",
	"CREATE TABLE", "CREATE INDEX", "TRUNCATE ", "GRANT ", "REVOKE ",
}

func assertReadOnlySQL(t *testing.T, query string) {
	t.Helper()
	upper := strings.ToUpper(query)
	if !strings.HasPrefix(strings.TrimSpace(upper), "SELECT") {
		t.Fatalf("query must start with SELECT: %s", query)
	}
	for _, keyword := range forbiddenSQL {
		if strings.Contains(upper, keyword) {
			t.Fatalf("read-only query must not contain %q: %s", keyword, query)
		}
	}
}

func TestBuildQueryIsSelectOnlyAndUsesTenantPlaceholder(t *testing.T) {
	for _, tenant := range []string{"", "tenant-a"} {
		scanQuery, scanArgs := buildTenantScanQuery(tenant)
		assertReadOnlySQL(t, scanQuery)
		inventoryQuery, inventoryArgs := buildInventoryQuery(tenant)
		assertReadOnlySQL(t, inventoryQuery)
		if got, want := strings.Count(inventoryQuery, "?"), len(inventoryArgs); got != want || got > 1 {
			t.Fatalf("inventory placeholder count = %d, args = %d, want equal and <= 1", got, want)
		}
		if got, want := strings.Count(scanQuery, "?"), len(scanArgs); got != want || got > 1 {
			t.Fatalf("scan placeholder count = %d, args = %d, want equal and <= 1", got, want)
		}
		if tenant == "" {
			if strings.Contains(inventoryQuery, "e.tenant_id = ?") {
				t.Fatalf("inventory query must not filter tenant when filter is empty: %s", inventoryQuery)
			}
			continue
		}
		if !strings.Contains(inventoryQuery, "e.tenant_id = ?") || inventoryArgs[0] != tenant {
			t.Fatalf("inventory query must bind tenant via placeholder, args = %v", inventoryArgs)
		}
		if !strings.Contains(scanQuery, "WHERE tenant_id = ?") || scanArgs[0] != tenant {
			t.Fatalf("scan query must bind tenant via placeholder, args = %v", scanArgs)
		}
	}
}

func TestBuildInventoryQueryCoversEveryFlaggedCategory(t *testing.T) {
	query, _ := buildInventoryQuery("")
	for _, expected := range []string{
		"LEFT JOIN pm_service_item AS si",
		"TRIM(e.service_item_id) = ''",
		"si.id IS NULL",
		"TRIM(e.file_id) = ''",
		"e.evidence_kind NOT IN ('FIELD','DEVIATION','REPORT')",
		"COALESCE(si.project_id, '')",
		"GROUP BY e.tenant_id, COALESCE(si.project_id, '')",
	} {
		if !strings.Contains(query, expected) {
			t.Fatalf("inventory query must contain %q: %s", expected, query)
		}
	}
}

func TestParseSampleIDs(t *testing.T) {
	ids, err := parseSampleIDs("10, 9,8")
	if err != nil {
		t.Fatalf("parseSampleIDs() error = %v", err)
	}
	if len(ids) != 3 || ids[0] != 10 || ids[1] != 9 || ids[2] != 8 {
		t.Fatalf("parseSampleIDs() = %v, want [10 9 8]", ids)
	}
	if ids, err := parseSampleIDs("  "); err != nil || ids != nil {
		t.Fatalf("parseSampleIDs(blank) = %v, %v, want nil, nil", ids, err)
	}
	ids, err = parseSampleIDs("1,2,3,4,5,6,7")
	if err != nil {
		t.Fatalf("parseSampleIDs(overflow) error = %v", err)
	}
	if len(ids) != sampleIDLimit {
		t.Fatalf("parseSampleIDs(overflow) length = %d, want %d", len(ids), sampleIDLimit)
	}
	for _, invalid := range []string{"1,abc", "1,,2", "0", "-3", "1,2,"} {
		if _, err := parseSampleIDs(invalid); err == nil {
			t.Fatalf("parseSampleIDs(%q) error = nil, want rejection", invalid)
		}
	}
}

func sampleReport() inventoryReport {
	return inventoryReport{
		Mode:         modeDryRun,
		ReadOnly:     true,
		GeneratedAt:  "2026-10-31T00:00:00Z",
		TenantFilter: "tenant-a",
		Scanned:      scanCounts{Total: 9, Flagged: 2},
		Categories: categoryCounts{
			MissingServiceItem: 1,
			UnknownServiceItem: 1,
			EmptyFileID:        0,
			UnexpectedKind:     0,
		},
		Tenants: []tenantStat{{
			TenantID:       "tenant-a",
			ScannedRecords: 9,
			FlaggedRecords: 2,
			Projects:       2,
			Categories: categoryCounts{
				MissingServiceItem: 1,
				UnknownServiceItem: 1,
			},
		}},
		Groups: []groupStat{
			{
				TenantID:          "tenant-a",
				ProjectID:         "",
				FlaggedRecords:    1,
				Categories:        categoryCounts{MissingServiceItem: 1},
				EarliestCreatedAt: "2026-09-01T08:00:00.000Z",
				LatestCreatedAt:   "2026-09-01T08:00:00.000Z",
				SampleRecordIDs:   []int64{11},
			},
			{
				TenantID:          "tenant-a",
				ProjectID:         "PJ-1",
				FlaggedRecords:    1,
				Categories:        categoryCounts{UnknownServiceItem: 1},
				EarliestCreatedAt: "2026-09-02T08:00:00.000Z",
				LatestCreatedAt:   "2026-09-02T08:00:00.000Z",
				SampleRecordIDs:   []int64{12},
			},
		},
	}
}

// forbiddenFields 是盘点输出中禁止出现的敏感/内容字段：文件内容、摘要、客户、
// 凭据等都不得进入日志或证据台账输出。
var forbiddenFields = []string{
	"file_name", "file_mime", "file_size", "file_sha256", "created_by",
	"customer", "contract", "dsn", "password", "secret", "token", "content",
}

func assertNoForbiddenFields(t *testing.T, output string) {
	t.Helper()
	lower := strings.ToLower(output)
	for _, field := range forbiddenFields {
		if strings.Contains(lower, field) {
			t.Fatalf("inventory output must not expose %q: %s", field, output)
		}
	}
}

func TestRenderJSONIsReadOnlyAndRedacted(t *testing.T) {
	var buffer bytes.Buffer
	if err := renderJSON(&buffer, sampleReport()); err != nil {
		t.Fatalf("renderJSON() error = %v", err)
	}
	assertNoForbiddenFields(t, buffer.String())
	var decoded map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("renderJSON() produced invalid JSON: %v", err)
	}
	if decoded["mode"] != modeDryRun || decoded["read_only"] != true {
		t.Fatalf("rendered mode/read_only = %v/%v, want DRY_RUN/true", decoded["mode"], decoded["read_only"])
	}
	groups, ok := decoded["groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("rendered groups = %v, want 2 entries", decoded["groups"])
	}
	first := groups[0].(map[string]any)
	if first["tenant_id"] != "tenant-a" || first["project_id"] != "" {
		t.Fatalf("first group tenant/project = %v/%v", first["tenant_id"], first["project_id"])
	}
}

func TestRenderTableShowsCountsWithoutSensitiveFields(t *testing.T) {
	var buffer bytes.Buffer
	if err := renderTable(&buffer, sampleReport()); err != nil {
		t.Fatalf("renderTable() error = %v", err)
	}
	output := buffer.String()
	assertNoForbiddenFields(t, output)
	for _, expected := range []string{"mode=DRY_RUN", "scanned_total=9", "flagged_records=2", "tenant-a", "PJ-1", "11", "12"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("table output must contain %q: %s", expected, output)
		}
	}
}

func TestRenderTableEmptyGroupsStillRendersHeader(t *testing.T) {
	report := inventoryReport{Mode: modeDryRun, ReadOnly: true}
	var buffer bytes.Buffer
	if err := renderTable(&buffer, report); err != nil {
		t.Fatalf("renderTable(empty) error = %v", err)
	}
	if !strings.Contains(buffer.String(), "SAMPLE_IDS") {
		t.Fatalf("empty table output must keep header: %s", buffer.String())
	}
}

func TestCategoryCountsAdd(t *testing.T) {
	got := categoryCounts{MissingServiceItem: 1, UnknownServiceItem: 2}.add(categoryCounts{EmptyFileID: 3, UnexpectedKind: 4})
	want := categoryCounts{MissingServiceItem: 1, UnknownServiceItem: 2, EmptyFileID: 3, UnexpectedKind: 4}
	if got != want {
		t.Fatalf("categoryCounts.add() = %+v, want %+v", got, want)
	}
}

func TestApplyRefusalIsExplicit(t *testing.T) {
	if !strings.Contains(applyRefusal, "需单独立项") {
		t.Fatalf("applyRefusal = %q, want explicit 需单独立项", applyRefusal)
	}
}

package auditoutbox

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/migrations"
)

// 真实数据库上 prj_audit_outbox 只由 migrations/*.sql 建表，
// 而 sqlite 单测用 AutoMigrate 按模型自动补列，任何"模型有列、迁移没建"的缺口
// 都会在测试里全部通过、到 MySQL 才以 1054 Unknown column 失败。
// 这里直接比对模型列与迁移声明，把该缺口挡在单测阶段。
func TestReadModelColumnsExistInMigrations(t *testing.T) {
	declared := migratedOutboxColumns(t)
	for _, column := range modelOutboxColumns(t) {
		if !declared[column] {
			t.Errorf("outboxRow maps column %q but no migration creates it; Claim/ListFailed would fail with MySQL 1054", column)
		}
	}
}

// request_id 是读取侧与平台关联用的可空列，必须由迁移显式声明，
// 不能依赖写路径或 AutoMigrate 补齐。
func TestRequestIDIsDeclaredByMigration(t *testing.T) {
	if !migratedOutboxColumns(t)["request_id"] {
		t.Fatal("prj_audit_outbox.request_id must be created by a migration")
	}
}

var (
	outboxCreateBlock = regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?prj_audit_outbox\s*\((.*?)\)\s*ENGINE`)
	outboxAddColumn   = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+prj_audit_outbox\s+ADD\s+COLUMN\s+([a-z_][a-z0-9_]*)`)
)

// migratedOutboxColumns 汇总迁移里 prj_audit_outbox 的全部列名。
func migratedOutboxColumns(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		sql := string(body)
		for _, block := range outboxCreateBlock.FindAllStringSubmatch(sql, -1) {
			for _, name := range createBlockColumns(block[1]) {
				columns[name] = true
			}
		}
		for _, match := range outboxAddColumn.FindAllStringSubmatch(sql, -1) {
			columns[strings.ToLower(match[1])] = true
		}
	}
	if len(columns) == 0 {
		t.Fatal("no migration declares prj_audit_outbox")
	}
	return columns
}

// createBlockColumns 从建表语句正文里取列名，跳过索引与约束定义。
func createBlockColumns(block string) []string {
	var names []string
	for _, line := range strings.Split(block, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(fields[0], ","))
		switch strings.ToUpper(name) {
		case "PRIMARY", "UNIQUE", "KEY", "INDEX", "CONSTRAINT", "FOREIGN", "FULLTEXT", "CHECK":
			continue
		}
		names = append(names, name)
	}
	return names
}

// modelOutboxColumns 取 outboxRow 的 gorm column 映射。
func modelOutboxColumns(t *testing.T) []string {
	t.Helper()
	rowType := reflect.TypeOf(outboxRow{})
	columns := make([]string, 0, rowType.NumField())
	for i := 0; i < rowType.NumField(); i++ {
		tag := rowType.Field(i).Tag.Get("gorm")
		for _, part := range strings.Split(tag, ";") {
			if name, ok := strings.CutPrefix(part, "column:"); ok && name != "" {
				columns = append(columns, name)
			}
		}
	}
	if len(columns) == 0 {
		t.Fatal("outboxRow has no gorm column mapping")
	}
	return columns
}

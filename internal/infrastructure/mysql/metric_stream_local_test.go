package mysql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/migrations"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// localStreamDB 只在明确回环local_fix专库运行生产迁移与指标读取验证。
// 参数：t 为测试生命周期，只读取LOCAL_METRIC_STREAM_DSN_FILE私有文件。
// 返回值：隔离数据库；未配置跳过，非专库或非私有文件连接前拒绝。
func localStreamDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := os.Getenv("LOCAL_METRIC_STREAM_DSN_FILE")
	if path == "" {
		t.Skip("explicit local stream DSN file required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private DSN file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("DSN read failed")
	}
	cfg, err := driver.ParseDSN(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("invalid DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" || cfg.Net != "tcp" || cfg.DBName != "local_fix_project" || !cfg.ParseTime {
		t.Fatal("explicit local_fix database required")
	}
	if err := migration.Run(t.Context(), cfg.FormatDSN(), migrations.Files); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("isolated database unavailable")
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { pool.Close() })
	return db
}

// streamTestRows 核对帧边界并提取真实生产者输出的全部事实。
// 参数：t 为测试上下文，raw 为完整有界协议。
// 返回值：全部事实JSON；缺end、计数或摘要不符时失败。
func streamTestRows(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	out := []json.RawMessage{}
	digest := sha256.New()
	ended := false
	sequence := 0
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		if len(line) > 1<<20 {
			t.Fatal("oversized frame")
		}
		var frame struct {
			Type     string
			Sequence int
			Rows     []json.RawMessage
			Count    int    `json:"row_count"`
			SHA      string `json:"sha256"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "chunk" {
			sequence++
			if frame.Sequence != sequence || len(frame.Rows) > 256 {
				t.Fatal("invalid chunk")
			}
			for _, row := range frame.Rows {
				digest.Write(row)
				digest.Write([]byte{'\n'})
				out = append(out, row)
			}
		}
		if frame.Type == "end" {
			ended = true
			if frame.Count != len(out) || frame.SHA != hex.EncodeToString(digest.Sum(nil)) {
				t.Fatal("incomplete stream")
			}
		}
	}
	if !ended {
		t.Fatal("missing end")
	}
	return out
}

// TestLocalProjectMetricStreamHistory 验证真实窗口关联SQL完整保留超万行首轮未知历史。
// 参数：t 为隔离测试上下文。
// 返回值：无；缺失未知行、归属错误或块不完整时失败。
func TestLocalProjectMetricStreamHistory(t *testing.T) {
	db := localStreamDB(t)
	tenant := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	now := time.Now().UTC()
	project := projectRecord{ID: tenant, TenantID: tenant, OwnerOrgID: "org-a", CreatedAt: now, UpdatedAt: now}
	item := serviceItemRecord{ID: tenant, TenantID: tenant, ProjectID: tenant, ProjectManagerID: "person-a", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Where("tenant_id=?", tenant).Delete(&projectRecord{})
	defer db.Where("tenant_id=?", tenant).Delete(&serviceItemRecord{})
	defer db.Where("tenant_id=?", tenant).Delete(&reportRevisionRecord{})
	for first := 0; first < 10001; first += 200 {
		last := first + 200
		if last > 10001 {
			last = 10001
		}
		rows := []reportRevisionRecord{}
		for i := first; i < last; i++ {
			rows = append(rows, reportRevisionRecord{TenantID: tenant, ServiceItemID: tenant, SubjectType: "SERVICE_ITEM", SubjectID: fmt.Sprint(i), ReviewRequired: false, Status: "COMPILING", ValidityStatus: "ACTIVE", CreatedAt: now, UpdatedAt: now})
		}
		if err := db.Create(&rows).Error; err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	repo := NewRepository(db)
	if err := repo.StreamMetricFacts(context.Background(), tenant, &output); err != nil {
		t.Fatal(err)
	}
	rows := streamTestRows(t, output.Bytes())
	if path := os.Getenv("LOCAL_METRIC_STREAM_OUTPUT_FILE"); path != "" {
		if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if len(rows) != 10001 {
		t.Fatalf("rows=%d", len(rows))
	}
	for _, raw := range rows {
		var row struct {
			Status   string
			Org      string `json:"organization_id"`
			Identity string `json:"identity_id"`
		}
		if err := json.Unmarshal(raw, &row); err != nil || row.Status != "UNKNOWN" || row.Org != "org-a" || row.Identity != "person-a" {
			t.Fatalf("unknown/scope changed %s", raw)
		}
	}
}

// TestLocalProjectMetricStreamResourceEquivalence 验证资源SQL聚合和旧协议在多组织、身份、月份及未知上完全等价。
// 参数：t 为显式隔离MySQL测试上下文。
// 返回值：无；任何资源行、分钟或未知计数改变时失败。
func TestLocalProjectMetricStreamResourceEquivalence(t *testing.T) {
	db := localStreamDB(t)
	tenant := fmt.Sprintf("resource-%d", time.Now().UnixNano())
	now := time.Now().UTC()
	defer db.Where("tenant_id=?", tenant).Delete(&capabilityRecord{})
	defer db.Where("tenant_id=?", tenant).Delete(&metricCapacityRecord{})
	defer db.Where("tenant_id=?", tenant).Delete(&metricUsageRecord{})
	current := metricPeriod(now)
	for i, org := range []string{"org-a", "org-b"} {
		capacity := metricCapacityRecord{domain.ResourceCapacity{ID: fmt.Sprintf("%s-c%d", tenant, i), TenantID: tenant, ResourceType: "PERSON", ResourceID: fmt.Sprint(i), IdentityID: "same-person", BusinessDate: fmt.Sprintf("%s-%02d", current, i+1), OrganizationID: org, Intervals: []domain.MetricInterval{}, Minutes: 240, Status: "PUBLISHED", CreatedAt: now}}
		if err := db.Create(&capacity).Error; err != nil {
			t.Fatal(err)
		}
		for j, status := range []string{"CONFIRMED", "PENDING", "CANCELLED"} {
			usage := metricUsageRecord{domain.ResourceUsage{ID: fmt.Sprintf("%s-u%d%d", tenant, i, j), TenantID: tenant, ResourceType: "PERSON", ResourceID: fmt.Sprint(i), IdentityID: "same-person", BusinessDate: fmt.Sprintf("%s-%02d", current, i+1), OrganizationID: org, Minutes: 60, Status: status, AutoConfirmed: status == "CONFIRMED", Start: now, End: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}}
			if err := db.Create(&usage).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, identity := range []string{"same-person", "unknown-person", "unknown-person"} {
		resource := capabilityRecord{ID: fmt.Sprintf("%s-r%d", tenant, i), TenantID: tenant, ResourceType: "PERSON", ResourceID: fmt.Sprint(i), UserID: identity, CapabilityCodes: []byte("[]"), Status: "ACTIVE", UpdatedAt: now}
		if err := db.Create(&resource).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepository(db)
	legacy, err := repo.ReadMetricFacts(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := repo.StreamMetricFacts(context.Background(), tenant, &output); err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.MetricFactRow{}
	for _, row := range legacy.Rows {
		want[row.Kind+":"+row.RecordID] = row
	}
	got := map[string]domain.MetricFactRow{}
	for _, raw := range streamTestRows(t, output.Bytes()) {
		var row domain.MetricFactRow
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		got[row.Kind+":"+row.RecordID] = row
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stream=%+v legacy=%+v", got, want)
	}
}

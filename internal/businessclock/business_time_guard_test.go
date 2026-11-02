package businessclock

// 本文件是「业务日历值按北京时间」改造的防回归守卫。
//
// 守卫只做两件事：读本仓库的非测试 Go 源码做静态断言，以及断言本包的契约；
// 不连数据库、不联网、不依赖当前时刻（真实时钟断言带一次跨零点重试），
// 因此可以在任何环境（含无 MySQL 的 CI）快速稳定执行。
//
// 被钉死的两条规则：
//  1. SQL 里不得出现依赖数据库会话时区的业务日来源（CURDATE()/UTC_DATE()/
//     CURRENT_DATE/NOW()）：这些函数取的是数据库会话时区的“当天”，与业务要求的
//     北京当日在北京时间 00:00–08:00 会差一天。本仓库的业务日一律在 Go 侧算好后
//     作为 ? 绑定参数传入。
//     注意：UTC_TIMESTAMP(3) 用于存储时间戳/租约是合法的，本守卫不禁；
//     DATEDIFF(?, col)、DATE(?) 这类“参数对列”的形态也是合法目标形态。
//  2. 不得从“当前时刻”直接用 UTC（或进程本地时区）派生日历年/月/日/yyyymmdd，
//     即 time.Now().UTC().Year()/Month()/Day()/Format("2006")/Format("20060102")
//     及其去掉 .UTC() 的变体；业务日历值必须走本包的 CalendarDay()/DateString()/
//     DateKey()/YearOf()。
//
// 排除规则（避免假阳性）：
//   - 只扫 *.go，且跳过 *_test.go（测试夹具里的 CURDATE() 不属于被测产品逻辑）；
//   - 跳过 vendor/、node_modules/、testdata/ 以及所有以 “.” 开头的目录
//     （.gocache/、.git/ 等）；
//   - 跳过本包目录 internal/businessclock/ 自身：本文件的注释和正则字面量里必然
//     出现禁用词，本包改由文件末尾的契约测试覆盖；
//   - 注释不算命中：用 go/parser 取出真实注释区间并在副本里涂空（保留行号），
//     所以包注释里提到 CURDATE() 的说明文字不会误报；字符串字面量里的 SQL
//     仍然参与扫描。

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// businessTimeRule 描述一条禁用模式。
type businessTimeRule struct {
	name    string         // 禁用模式的可读名称，出现在失败信息里
	pattern *regexp.Regexp // 命中规则
	advice  string         // 应改成的目标形态
}

// businessTimeSQLRules 是「SQL 不得从数据库会话时区取业务日」的规则集。
//
// 大小写处理：CURDATE/UTC_DATE/CURRENT_DATE 用大小写不敏感匹配，Go 里不存在
// 同名标识符，不会误伤；NOW 必须大小写敏感，否则会命中 time.Now()。
func businessTimeSQLRules() []businessTimeRule {
	return []businessTimeRule{
		{
			name:    "CURDATE()",
			pattern: regexp.MustCompile(`(?i)\bCURDATE\b`),
			advice:  "把 Go 侧业务日（businessclock.CalendarDay(time.Now())/DateKey）作为 ? 绑定参数，例如 DATEDIFF(?, due_date)",
		},
		{
			name:    "UTC_DATE()",
			pattern: regexp.MustCompile(`(?i)\bUTC_DATE\b`),
			advice:  "把 Go 侧业务日（businessclock.CalendarDay(time.Now())）作为 ? 绑定参数",
		},
		{
			name:    "CURRENT_DATE",
			pattern: regexp.MustCompile(`(?i)\bCURRENT_DATE\b`),
			advice:  "把 Go 侧业务日（businessclock.CalendarDay(time.Now())）作为 ? 绑定参数",
		},
		{
			name:    "NOW()（会话时钟）",
			pattern: regexp.MustCompile(`\bNOW\s*\(`),
			advice:  "业务日不要取数据库会话“现在”：传 businessclock 算好的绑定参数；确需写存储时间戳请用 UTC_TIMESTAMP(3)",
		},
	}
}

// businessTimeWallClockRules 是「不得从当前时刻直接派生业务日历值」的规则集。
//
// 只锁定“纯日历日/年”的两种布局（2006、20060102）：带时分秒的展示串
// （如 Format("2006-01-02 15:04")）和存储时间戳不在禁止范围内。
func businessTimeWallClockRules() []businessTimeRule {
	return []businessTimeRule{
		{
			name:    "time.Now().UTC().Year()/Month()/Day()",
			pattern: regexp.MustCompile(`time\.Now\(\)\s*\.\s*UTC\(\)\s*\.\s*(?:Year|Month|Day)\(\)`),
			advice:  "业务年/月/日改用 businessclock.YearOf(time.Now())/CalendarDay(time.Now())/Now().Year()",
		},
		{
			name:    `time.Now().UTC().Format("2006")/Format("20060102")`,
			pattern: regexp.MustCompile(`time\.Now\(\)\s*\.\s*UTC\(\)\s*\.\s*Format\(\s*"(?:2006|20060102)"\s*\)`),
			advice:  "业务日/业务年文本改用 businessclock.DateString(time.Now())/DateKey(time.Now())",
		},
		{
			name:    "time.Now().Year()/Month()/Day()",
			pattern: regexp.MustCompile(`time\.Now\(\)\s*\.\s*(?:Year|Month|Day)\(\)`),
			advice:  "进程本地时区不等于业务时区，改用 businessclock.YearOf(time.Now())/CalendarDay(time.Now())",
		},
		{
			name:    `time.Now().Format("2006")/Format("20060102")`,
			pattern: regexp.MustCompile(`time\.Now\(\)\s*\.\s*Format\(\s*"(?:2006|20060102)"\s*\)`),
			advice:  "业务日/业务年文本改用 businessclock.DateString(time.Now())/DateKey(time.Now())",
		},
	}
}

// businessTimeViolation 是一条扫描命中。
type businessTimeViolation struct {
	relPath string // 相对仓库根的路径
	line    int    // 行号（注释已被涂空，行号与源文件一致）
	snippet string // 命中行源码（已裁剪）
	rule    businessTimeRule
}

// TestBusinessTimeGuardNoSessionClockInSQL 断言本仓库非测试源码的 SQL 不再
// 用数据库会话时区派生业务日。
func TestBusinessTimeGuardNoSessionClockInSQL(t *testing.T) {
	reportBusinessTimeViolations(t, scanBusinessTimeViolations(t, businessTimeSQLRules()))
}

// TestBusinessTimeGuardNoCalendarFromWallClock 断言本仓库非测试源码不再从
// 当前时刻直接派生业务日历值。
func TestBusinessTimeGuardNoCalendarFromWallClock(t *testing.T) {
	reportBusinessTimeViolations(t, scanBusinessTimeViolations(t, businessTimeWallClockRules()))
}

// TestGuardedCalendarDayIsBeijingDayWithUTCWallClock 钉死 CalendarDay() 的契约：
// 日历日是北京“当天”，但时间部分是 00:00:00、Location 必须是 time.UTC，
// 否则与 DATE 列（parseTime 后是 UTC 零点）比较时整体偏移 8 小时。
func TestGuardedCalendarDayIsBeijingDayWithUTCWallClock(t *testing.T) {
	// 1) 固定时刻验证跨零点语义：2025-12-31T16:30Z 已是北京 2026-01-01 00:30。
	instant := time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)
	calendarDay := CalendarDay(instant)
	if calendarDay.Location() != time.UTC {
		t.Fatalf("CalendarDay(%s).Location()=%v；必须是 time.UTC，否则与 DATE 列比较会偏移",
			instant.Format(time.RFC3339), calendarDay.Location())
	}
	if got := calendarDay.Format("2006-01-02 15:04:05"); got != "2026-01-01 00:00:00" {
		t.Fatalf("CalendarDay(%s) 墙钟=%s；必须是北京日历日 2026-01-01 00:00:00",
			instant.Format(time.RFC3339), got)
	}
	if h, m, s := calendarDay.Clock(); h != 0 || m != 0 || s != 0 || calendarDay.Nanosecond() != 0 {
		t.Fatalf("CalendarDay() 时间部分=%02d:%02d:%02d.%09d；必须是 00:00:00.000000000",
			h, m, s, calendarDay.Nanosecond())
	}
	// 已经是 UTC 零点的 DATE 列值必须原样归日，不能被二次偏移到前一天。
	if back := CalendarDay(calendarDay); !back.Equal(calendarDay) {
		t.Fatalf("CalendarDay(DATE 零点)=%s；必须幂等", back.Format("2006-01-02 15:04:05"))
	}

	// 2) 真实时钟：CalendarDay(time.Now()) 必须落在北京“当天”。
	// 跨零点时先取的时刻可能已经过期，允许重算一次，避免午夜抖动造成假失败。
	now := time.Now()
	calendarDay = CalendarDay(now)
	if got, want := DateString(now), calendarDay.Format("20060102"); got != want {
		now = time.Now()
		calendarDay = CalendarDay(now)
		if got, want = DateString(now), calendarDay.Format("20060102"); got != want {
			t.Fatalf("DateString(now)=%s；CalendarDay(now)=%s", got, want)
		}
	}
	if calendarDay.Location() != time.UTC {
		t.Fatalf("真实时钟下 CalendarDay(now).Location()=%v；必须是 time.UTC", calendarDay.Location())
	}
	if _, offset := now.In(Location()).Zone(); offset != ZoneOffsetSeconds {
		t.Fatalf("业务时区偏移=%d 秒；必须是 %d（北京时间）", offset, ZoneOffsetSeconds)
	}
	// DateKey / YearOf 必须与 CalendarDay 同一口径。
	if got, want := DateKey(now), calendarDay.Format("2006-01-02"); got != want {
		t.Fatalf("DateKey(now)=%s；CalendarDay(now)=%s（应同一业务日）", got, want)
	}
	if got, want := YearOf(now), calendarDay.Year(); got != want {
		t.Fatalf("YearOf(now)=%d；CalendarDay(now).Year()=%d（应同一业务年）", got, want)
	}
}

// scanBusinessTimeViolations 遍历仓库非测试 Go 文件，返回命中 rules 的位置。
func scanBusinessTimeViolations(t *testing.T, rules []businessTimeRule) []businessTimeViolation {
	t.Helper()
	root := guardRepositoryRoot(t)
	var violations []businessTimeViolation
	scannedFiles := 0
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relPath = filepath.ToSlash(relPath)
		if entry.IsDir() {
			if guardSkipDir(entry.Name(), relPath) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scannedFiles++
		violations = append(violations, scanBusinessTimeSource(t, relPath, source, rules)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描源码失败：%v", walkErr)
	}
	// 覆盖度自检：目录定位错误或排除规则写宽了会让扫描“空跑通过”，这里直接失败。
	if scannedFiles < 50 {
		t.Fatalf("只扫描到 %d 个非测试 Go 文件（仓库根 %s），预期远大于此；请检查仓库根定位与排除规则", scannedFiles, root)
	}
	return violations
}

// TestBusinessTimeGuardScanLogicItself 是守卫自身的正向控制（变异自证）：
// 用一段含违规写法的假源码确认扫描确实会命中，同时确认注释里的禁用词不误报。
// 这样“扫描永远返回空”的实现缺陷不会伪装成通过。
func TestBusinessTimeGuardScanLogicItself(t *testing.T) {
	source := []byte(`package sample

// 说明文字里提到 CURDATE() 和 time.Now().UTC().Year() 不算命中。
const sessionClockSQL = "SELECT 1 WHERE due_date < CURDATE()"
const storageSQL = "INSERT INTO t(stamp) VALUES(UTC_TIMESTAMP(3))"

func badYear() int { return time.Now().UTC().Year() }

func goodSQL() string { return "SELECT DATEDIFF(?, due_date) FROM t WHERE due_date < ?" }

/* 块注释里的 NOW() 与 UTC_DATE() 同样不算命中 */
`)
	sqlViolations := scanBusinessTimeSource(t, "sample.go", source, businessTimeSQLRules())
	if len(sqlViolations) != 1 {
		t.Fatalf("SQL 规则命中 %d 处；want 1（sessionClockSQL 的 CURDATE()）", len(sqlViolations))
	}
	if sqlViolations[0].line != 4 || sqlViolations[0].rule.name != "CURDATE()" {
		t.Fatalf("SQL 规则命中 %s:%d「%s」；want sample.go:4「CURDATE()」",
			sqlViolations[0].relPath, sqlViolations[0].line, sqlViolations[0].rule.name)
	}

	wallClockViolations := scanBusinessTimeSource(t, "sample.go", source, businessTimeWallClockRules())
	if len(wallClockViolations) != 1 {
		t.Fatalf("时钟规则命中 %d 处；want 1（badYear 的 time.Now().UTC().Year()）", len(wallClockViolations))
	}
	if wallClockViolations[0].line != 7 {
		t.Fatalf("时钟规则命中 line=%d；want 7", wallClockViolations[0].line)
	}
}

// scanBusinessTimeSource 在单个文件里找出命中规则的“非注释”行。
func scanBusinessTimeSource(t *testing.T, relPath string, source []byte, rules []businessTimeRule) []businessTimeViolation {
	t.Helper()
	code := guardCodeWithoutComments(t, relPath, source)
	var violations []businessTimeViolation
	for index, line := range strings.Split(string(code), "\n") {
		for _, rule := range rules {
			if !rule.pattern.MatchString(line) {
				continue
			}
			violations = append(violations, businessTimeViolation{
				relPath: relPath,
				line:    index + 1,
				snippet: guardClip(strings.TrimSpace(line), 200),
				rule:    rule,
			})
			break // 同一行只报一次，避免一条 SQL 刷出多条规则
		}
	}
	return violations
}

// guardCodeWithoutComments 用 go/parser 取出真实注释区间，在副本里把注释涂成
// 空格（保留换行，行号不变），返回只剩代码与字符串字面量的源码。
// 相比“按行截断 //”，这里不会把字符串里的 // 或 /* */ 误判成注释。
func guardCodeWithoutComments(t *testing.T, relPath string, source []byte) []byte {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, relPath, source, parser.ParseComments)
	if err != nil {
		t.Fatalf("%s 解析失败，守卫无法判断注释边界：%v", relPath, err)
	}
	code := make([]byte, len(source))
	copy(code, source)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			start := fileSet.Position(comment.Slash).Offset
			if start < 0 || start >= len(code) {
				continue
			}
			end := start + len(comment.Text)
			if end > len(code) {
				end = len(code)
			}
			for i := start; i < end; i++ {
				if code[i] != '\n' {
					code[i] = ' '
				}
			}
		}
	}
	return code
}

// guardSkipDir 报告哪些目录不参与扫描。
func guardSkipDir(name string, relPath string) bool {
	if strings.HasPrefix(name, ".") { // .git/.gocache/.ai 等
		return true
	}
	switch name {
	case "vendor", "node_modules", "testdata":
		return true
	}
	// 本包自身：本文件的注释/正则里必然出现禁用词，改由契约测试覆盖。
	return relPath == "internal/businessclock"
}

// guardRepositoryRoot 由测试工作目录（包目录）回推仓库根，并校验 go.mod。
func guardRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根 %s 没有 go.mod（测试工作目录可能不是 internal/businessclock）：%v", root, err)
	}
	return root
}

// reportBusinessTimeViolations 输出中文失败信息：文件:行号 + 禁用模式 + 应改成什么。
func reportBusinessTimeViolations(t *testing.T, violations []businessTimeViolation) {
	t.Helper()
	const limit = 20
	for index, violation := range violations {
		if index >= limit {
			t.Errorf("还有 %d 处未列出（共 %d 处）", len(violations)-limit, len(violations))
			break
		}
		t.Errorf("%s:%d 命中禁用模式「%s」\n  源码：%s\n  应改成：%s",
			violation.relPath, violation.line, violation.rule.name, violation.snippet, violation.rule.advice)
	}
	if len(violations) > 0 {
		t.Errorf("业务日历值必须按北京时间在 Go 侧计算后作为 SQL 参数绑定，详见 internal/businessclock 包注释")
	}
}

// guardClip 按 rune 截断，避免失败信息过长。
func guardClip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

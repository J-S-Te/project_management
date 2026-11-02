// Package businessclock 提供业务日历口径的时钟。
//
// 存储与时间戳（created_at、事件时刻、token/租约、SLA 小时级判定）仍统一使用 UTC；
// 但业务日历值——业务日/业务年、到期判定、编号里的日期段——必须按北京时间（UTC+8）计算，
// 否则在北京时间 00:00–08:00 之间会把“今天”算成前一天，甚至把跨年业务写进上一年的编号。
package businessclock

import (
	"sync"
	"time"
)

const (
	// ZoneName 是业务时区。优先加载 IANA 时区库，加载失败时回落到固定 +8 偏移。
	ZoneName = "Asia/Shanghai"
	// ZoneOffsetSeconds 是北京时间相对 UTC 的偏移秒数。
	ZoneOffsetSeconds = 8 * 3600

	dateLayout = "20060102"
)

// location 只解析一次：时区库读取有文件 IO 开销，且结果在整个进程内不变。
var location = sync.OnceValue(func() *time.Location {
	if zone, err := time.LoadLocation(ZoneName); err == nil && zone != nil {
		return zone
	}
	return time.FixedZone("CST", ZoneOffsetSeconds)
})

// Location 返回业务时区，供需要直接格式化或展示的调用方复用同一口径。
func Location() *time.Location {
	return location()
}

// Now 返回北京时间的当前时刻。
func Now() time.Time {
	return time.Now().In(Location())
}

// YearOf 返回 t 在北京时区所属的业务年。
func YearOf(t time.Time) int {
	return t.In(Location()).Year()
}

// DateString 返回 t 在北京时区所属业务日的 yyyyMMdd 文本。
func DateString(t time.Time) string {
	return t.In(Location()).Format(dateLayout)
}

// DateKey 返回 t 在北京时区所属业务日的 yyyy-MM-dd 文本，
// 用于与 DATE 列同一形态的日期文本比较、以及人类可读的去重键/日志。
func DateKey(t time.Time) string {
	return t.In(Location()).Format("2006-01-02")
}

// CalendarDay 返回 t 在北京时区所属日历日的零点。
//
// 返回值固定在 time.UTC，便于与 DATE 列直接比较：DATE 列（parseTime 后是 UTC 零点）
// 与计划/时段文本（time.Parse("2006-01-02") 同样得到 UTC 零点）都落在同一套“墙钟日期”上，
// 而原始时间戳按北京时区归日，跨零点后不会错位到前一天。
func CalendarDay(t time.Time) time.Time {
	local := t.In(Location())
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

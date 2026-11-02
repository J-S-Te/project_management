package businessclock

import (
	"testing"
	"time"
)

// 北京时间与 UTC 跨日：UTC 16:30 = 北京次日 00:30。
func TestDateStringUsesBeijingCalendarDay(t *testing.T) {
	tests := []struct {
		name     string
		instant  time.Time
		date     string
		dateKey  string
		year     int
		calendar time.Time
	}{
		{
			name:     "UTC late evening rolls over to Beijing next day",
			instant:  time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC),
			date:     "20260101",
			dateKey:  "2026-01-01",
			year:     2026,
			calendar: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "UTC afternoon stays on the same Beijing day",
			instant:  time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC),
			date:     "20251231",
			dateKey:  "2025-12-31",
			year:     2025,
			calendar: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DateString(test.instant); got != test.date {
				t.Fatalf("DateString(%s) = %q, want %q", test.instant, got, test.date)
			}
			if got := DateKey(test.instant); got != test.dateKey {
				t.Fatalf("DateKey(%s) = %q, want %q", test.instant, got, test.dateKey)
			}
			if got := YearOf(test.instant); got != test.year {
				t.Fatalf("YearOf(%s) = %d, want %d", test.instant, got, test.year)
			}
			if got := CalendarDay(test.instant); !got.Equal(test.calendar) {
				t.Fatalf("CalendarDay(%s) = %s, want %s", test.instant, got, test.calendar)
			}
			if loc := CalendarDay(test.instant).Location(); loc != time.UTC {
				t.Fatalf("CalendarDay location = %s, want UTC for DATE column comparison", loc)
			}
		})
	}
}

// 已经按 UTC 零点解析的 DATE 列值必须原样归日，不能被二次偏移推到前一天或后一天。
func TestCalendarDayKeepsUTCDateValuesStable(t *testing.T) {
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := CalendarDay(date); !got.Equal(date) {
		t.Fatalf("CalendarDay(DATE value) = %s, want %s", got, date)
	}
	if got := DateString(date); got != "20260101" {
		t.Fatalf("DateString(DATE value) = %q, want 20260101", got)
	}
}

func TestNowAndLocationUseBeijingOffset(t *testing.T) {
	if _, offset := Now().Zone(); offset != ZoneOffsetSeconds {
		t.Fatalf("Now() offset = %d, want %d", offset, ZoneOffsetSeconds)
	}
	if _, offset := time.Now().In(Location()).Zone(); offset != ZoneOffsetSeconds {
		t.Fatalf("Location() offset = %d, want %d", offset, ZoneOffsetSeconds)
	}
}

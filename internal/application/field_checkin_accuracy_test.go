package application

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestCheckInRejectsUnreliableCoordinates 防止粗定位和非有限数值形成签到事件。
// 参数：t 为测试上下文，逐项使用隔离内存仓储校验输入。
// 返回值：无；应拒绝的输入未被拒绝或写入事件时测试失败。
func TestCheckInRejectsUnreliableCoordinates(t *testing.T) {
	cases := []struct {
		name   string
		change func(*domain.FieldCheckInInput)
	}{
		{"accuracy_over_100m", func(in *domain.FieldCheckInInput) { in.AccuracyMeters = 100.01 }},
		{"city_level_accuracy", func(in *domain.FieldCheckInInput) { in.AccuracyMeters = 5000 }},
		{"zero_accuracy", func(in *domain.FieldCheckInInput) { in.AccuracyMeters = 0 }},
		{"negative_accuracy", func(in *domain.FieldCheckInInput) { in.AccuracyMeters = -1 }},
		{"nan_accuracy", func(in *domain.FieldCheckInInput) { in.AccuracyMeters = math.NaN() }},
		{"infinite_accuracy", func(in *domain.FieldCheckInInput) { in.AccuracyMeters = math.Inf(1) }},
		{"nan_latitude", func(in *domain.FieldCheckInInput) { in.Latitude = math.NaN() }},
		{"nan_longitude", func(in *domain.FieldCheckInInput) { in.Longitude = math.NaN() }},
		{"infinite_latitude", func(in *domain.FieldCheckInInput) { in.Latitude = math.Inf(1) }},
		{"infinite_longitude", func(in *domain.FieldCheckInInput) { in.Longitude = math.Inf(-1) }},
		{"latitude_out_of_range", func(in *domain.FieldCheckInInput) { in.Latitude = 90.01 }},
		{"longitude_out_of_range", func(in *domain.FieldCheckInInput) { in.Longitude = -180.01 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repository := checkInRepository()
			service := &Service{Repo: repository}
			input := checkInInput(hangzhouLatitude, hangzhouLongitude, "现场签到")
			tc.change(&input)
			err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID, input)
			if !errors.Is(err, ErrValidation) || len(repository.events) != 0 {
				t.Fatalf("unreliable input: err=%v, events=%d; want validation and no event", err, len(repository.events))
			}
		})
	}
}

// TestCheckInAcceptsAccuracyBoundary 保证一百米精度边界内的正常签到仍可成功。
// 参数：t 为测试上下文，使用有效站点与已授权工程师。
// 返回值：无；边界值被拒绝或未记录真实精度时测试失败。
func TestCheckInAcceptsAccuracyBoundary(t *testing.T) {
	repository := checkInRepository()
	service := &Service{Repo: repository}
	input := checkInInput(hangzhouLatitude, hangzhouLongitude, "")
	input.AccuracyMeters = 100
	err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID, input)
	if err != nil || len(repository.events) != 1 || repository.events[0].Payload["accuracy_meters"] != float64(100) {
		t.Fatalf("boundary check-in: err=%v, events=%+v", err, repository.events)
	}
}

package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpiredSLAScanStopsBeforeRepositoryButDispatcherRemainsSeparate(t *testing.T) {
	denied := errors.New("expired")
	s := &Service{CheckBusinessLicense: func(context.Context) error { return denied }}
	if n, err := s.ScanSlaNotifications(context.Background(), "t1", time.Now()); n != 0 || !errors.Is(err, denied) {
		t.Fatal(n, err)
	}
}

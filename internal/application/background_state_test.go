package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

type backgroundLeaseRepository struct {
	Repository
	n      int64
	err    error
	tenant string
}

func (r *backgroundLeaseRepository) CountActiveBackgroundLeases(_ context.Context, tenant string, _ time.Time) (int64, error) {
	r.tenant = tenant
	return r.n, r.err
}
func TestBackgroundLeaseInspectionFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	s := &Service{}
	if n, e := s.CountActiveBackgroundLeases(context.Background(), "tenant", now); e == nil || n != -1 {
		t.Fatal(n, e)
	}
	r := &backgroundLeaseRepository{n: 2}
	s.Repo = r
	if n, e := s.CountActiveBackgroundLeases(context.Background(), "tenant-a", now); e != nil || n != 2 || r.tenant != "tenant-a" {
		t.Fatal(n, e, r.tenant)
	}
	for _, tc := range []struct {
		n int64
		e error
	}{{-1, nil}, {0, errors.New("db failure")}} {
		r.n, r.err = tc.n, tc.e
		if n, e := s.CountActiveBackgroundLeases(context.Background(), "tenant-a", now); e == nil || n != -1 {
			t.Fatal(n, e)
		}
	}
	if n, e := s.CountActiveBackgroundLeases(context.Background(), " tenant", now); e == nil || n != -1 {
		t.Fatal(n, e)
	}
}

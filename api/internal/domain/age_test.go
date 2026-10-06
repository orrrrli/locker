package domain

import (
	"errors"
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCheckAge(t *testing.T) {
	today := date("2026-10-06")
	tests := []struct {
		name  string
		birth time.Time
		today time.Time
		want  error
	}{
		{"missing", time.Time{}, today, ErrBirthDateRequired},
		{"15 today", date("2011-10-06"), today, nil},
		{"one day short of 15", date("2011-10-07"), today, ErrUnderage},
		{"16 (treated as adult)", date("2010-01-01"), today, nil},
		{"adult", date("1990-05-20"), today, nil},
		{"in the future", date("2026-10-07"), today, ErrBirthDateInFuture},
		{"leap day, 28 Feb of non-leap year", date("2012-02-29"), date("2027-02-28"), ErrUnderage},
		{"leap day, 1 Mar of non-leap year", date("2012-02-29"), date("2027-03-01"), nil},
		{"time of day is ignored", date("2011-10-06"), today.Add(30 * time.Second), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckAge(tt.birth, tt.today); !errors.Is(err, tt.want) {
				t.Fatalf("CheckAge = %v, want %v", err, tt.want)
			}
		})
	}
}

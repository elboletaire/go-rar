package rar

import (
	"testing"
	"time"
)

func TestTimeToDOS(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want uint32
	}{
		{"epoch", time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC), 0x00210000},
		{"zero time clamps to epoch", time.Time{}, 0x00210000},
		{"pre-1980 clamps to epoch", time.Date(1979, 12, 31, 23, 59, 59, 0, time.UTC), 0x00210000},
		{"after 2107 clamps to latest", time.Date(2108, 1, 1, 0, 0, 0, 0, time.UTC), 0xFF9FBF7D},
		{"latest representable", time.Date(2107, 12, 31, 23, 59, 58, 0, time.UTC), 0xFF9FBF7D},
		{"fields", time.Date(2024, 10, 17, 13, 45, 58, 0, time.UTC),
			44<<25 | 10<<21 | 17<<16 | 13<<11 | 45<<5 | 29},
		{"odd second rounds down", time.Date(2024, 10, 17, 13, 45, 59, 0, time.UTC),
			44<<25 | 10<<21 | 17<<16 | 13<<11 | 45<<5 | 29},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeToDOS(tt.in); got != tt.want {
				t.Errorf("timeToDOS(%v) = %#08x, want %#08x", tt.in, got, tt.want)
			}
		})
	}
}

func TestDOSTimeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{"even seconds", time.Date(2024, 10, 17, 13, 45, 58, 0, time.UTC),
			time.Date(2024, 10, 17, 13, 45, 58, 0, time.UTC)},
		{"odd seconds truncate", time.Date(2000, 2, 29, 12, 0, 59, 999, time.UTC),
			time.Date(2000, 2, 29, 12, 0, 58, 0, time.UTC)},
		{"epoch", time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"zero time becomes epoch", time.Time{},
			time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"own location is kept", time.Date(2024, 10, 17, 10, 0, 0, 0, time.FixedZone("", 2*3600)),
			time.Date(2024, 10, 17, 10, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dosToTime(timeToDOS(tt.in))
			if !got.Equal(tt.want) {
				t.Errorf("round trip of %v = %v, want %v", tt.in, got, tt.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("location = %v, want UTC", got.Location())
			}
		})
	}
}

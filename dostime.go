package rar

import "time"

// MS-DOS date and time limits: 1980-01-01 00:00:00 up to 2107-12-31 23:59:58,
// the last value that fits the 7-bit year field.
const (
	dosEpoch  = 1<<21 | 1<<16                                   // 1980-01-01 00:00:00
	dosLatest = 127<<25 | 12<<21 | 31<<16 | 23<<11 | 59<<5 | 29 // 2107-12-31 23:59:58
)

// timeToDOS encodes t as an MS-DOS date and time in t's own location. Times
// outside the representable range are clamped; the zero time maps to 1980.
func timeToDOS(t time.Time) uint32 {
	if t.Year() < 1980 {
		return dosEpoch
	}
	if t.Year() > 2107 {
		return dosLatest
	}
	// Seconds are stored as second/2, so odd seconds round down.
	return uint32(t.Year()-1980)<<25 | uint32(t.Month())<<21 | uint32(t.Day())<<16 |
		uint32(t.Hour())<<11 | uint32(t.Minute())<<5 | uint32(t.Second()/2)
}

// dosToTime decodes an MS-DOS date and time. The result is in UTC because the
// format carries no zone.
func dosToTime(dos uint32) time.Time {
	return time.Date(
		int(dos>>25)+1980,
		time.Month(dos>>21&0xF),
		int(dos>>16&0x1F),
		int(dos>>11&0x1F),
		int(dos>>5&0x3F),
		int(dos&0x1F)*2,
		0,
		time.UTC,
	)
}

package rar

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEncodeNameASCII(t *testing.T) {
	for _, name := range []string{"01 Chapter.jpg", `dir\file.txt`} {
		b, unicode := encodeName(name)
		if unicode {
			t.Errorf("encodeName(%q) set the Unicode flag", name)
		}
		if string(b) != name {
			t.Errorf("encodeName(%q) = %q, want the bytes unchanged", name, b)
		}
	}
}

func TestEncodeNameBytes(t *testing.T) {
	tests := []struct {
		name string
		want []byte
	}{
		// Ñ is U+00D1, below 0x100, so it is opcode 0 and one byte.
		// Layout: UTF-8 (C3 91), zero, HIGH 00, flag 00, data D1.
		{"Ñ", []byte{0xC3, 0x91, 0x00, 0x00, 0x00, 0xD1}},

		// U+1F600 is the surrogate pair D83D DE00. Flag A0 is opcode 2 in
		// the first two slots. The ".png" units are opcode 0 in a second
		// group with flag 00.
		{"😀.png", []byte{
			0xF0, 0x9F, 0x98, 0x80, 0x2E, 0x70, 0x6E, 0x67, // UTF-8 fallback
			0x00,                                     // separator
			0x00,                                     // HIGH
			0xA0, 0x3D, 0xD8, 0x00, 0xDE, 0x2E, 0x70, // group 1
			0x00, 0x6E, 0x67, // group 2
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, unicode := encodeName(tt.name)
			if !unicode {
				t.Fatal("encodeName did not set the Unicode flag")
			}
			if !bytes.Equal(b, tt.want) {
				t.Errorf("encodeName(%q) = % X, want % X", tt.name, b, tt.want)
			}
		})
	}
}

// roundTripNames cover every opcode the encoder emits, group boundaries and
// surrogate pairs.
var roundTripNames = []string{
	"Ñandú\\ページ 01.jpg",
	"😀.png",
	"Ñ",
	"ページ",
	"a/b/ü.txt",
	"emoji 🎉🎉🎉 mixed ß",
	strings.Repeat("é", 9),
}

func TestNameRoundTrip(t *testing.T) {
	for _, name := range roundTripNames {
		t.Run(name, func(t *testing.T) {
			b, unicode := encodeName(name)
			if !unicode {
				t.Fatal("encodeName did not set the Unicode flag")
			}
			// The UTF-8 fallback comes first, then a zero byte, with no
			// zero byte inside the fallback.
			if !bytes.HasPrefix(b, []byte(name)) || b[len(name)] != 0 {
				t.Fatalf("fallback is not the UTF-8 name followed by zero: % X", b)
			}
			if bytes.IndexByte(b[:len(name)], 0) >= 0 {
				t.Fatal("fallback contains a zero byte")
			}
			if got := decodeName(b, unicode); got != name {
				t.Errorf("decodeName(encodeName(%q)) = %q", name, got)
			}
		})
	}
}

func TestDecodeName(t *testing.T) {
	tests := []struct {
		name    string
		field   []byte
		unicode bool
		want    string
	}{
		{"no unicode flag keeps bytes", []byte{0xD1, 'a'}, false, "\xD1a"},
		{"unicode without zero is UTF-8", []byte("Ñandú"), true, "Ñandú"},

		// Opcode 1 takes one byte and adds HIGH<<8. Field: "x", zero, HIGH
		// 0x30, flag 0x40 (opcode 1, then opcode 0), data 0x42 0x41. The
		// units are 0x3042 (あ) and 0x41 (A).
		{"opcode 1 with HIGH byte", []byte{'x', 0, 0x30, 0x40, 0x42, 0x41}, true, "あA"},

		// Opcode 1 with HIGH 0x00 and byte D1 gives U+00D1 (Ñ). The next
		// slot is opcode 0, but no data is left, so decoding stops.
		{"opcode 1 with zero HIGH", []byte{'x', 0, 0x00, 0x40, 0xD1}, true, "Ñ"},

		// Opcode 2 reads two bytes, low first: 0x42 | 0x30<<8 = 0x3042 (あ).
		{"opcode 2 word", []byte{'x', 0, 0x00, 0x80, 0x42, 0x30}, true, "あ"},

		// Opcode 2 pair D83D DE00 is U+1F600 (😀). Flag 0xA0 is opcode 2 in
		// the first two slots.
		{"opcode 2 surrogate pair", []byte{'x', 0, 0x00, 0xA0, 0x3D, 0xD8, 0x00, 0xDE}, true, "😀"},

		// Opcode 3 copies L+2 chars from the ASCII part at the output position.
		// Flag 0xFF: L=01 copies "rar", then L=00 copies "-f", "il" and "e";
		// the last run stops at the end of the ASCII part.
		{"opcode 3 copy runs", []byte{'r', 'a', 'r', '-', 'f', 'i', 'l', 'e', 0, 0x00,
			0xFF, 0x01, 0x00, 0x00, 0x00}, true, "rar-file"},

		// Opcode 3 with bit 0x80 set reads correction C = 0x30 and emits
		// (L&0x7F)+2 = 2 chars: (ascii[pos]+C) | HIGH<<8. 'a'+0x30 = 0x91 (Α),
		// 'b'+0x30 = 0x92 (Β).
		{"opcode 3 with correction", []byte{'a', 'b', 'c', 0, 0x03, 0xC0, 0x80, 0x30}, true, "ΑΒ"},

		// The correction sum wraps to a byte before HIGH is OR-ed in.
		// 'z'+0xA0 = 0x11A becomes 0x1A, and 'a'+0xA0 = 0x101 becomes 0x01.
		// With HIGH 0x03 that gives U+031A and U+0301.
		{"opcode 3 correction overflows", []byte{'z', 'a', 0, 0x03, 0xC0, 0x80, 0xA0}, true, "́̚"},

		// Flag 0xFF with two correction runs of 2 chars each needs four ASCII
		// chars, but "abc" has three. The second run emits 'c'+0x30 (Γ), then
		// stops at the end of the ASCII part instead of panicking.
		{"opcode 3 stops at ASCII end", []byte{'a', 'b', 'c', 0, 0x03, 0xFF, 0x80, 0x30, 0x80, 0x30},
			true, "ΑΒΓ"},

		// The ASCII part is returned when the UTF-16 part is missing or empty.
		{"zero with no UTF-16 part", []byte{'A', 0}, true, "A"},
		{"zero, HIGH and no data", []byte{'A', 0, 0x00}, true, "A"},
		{"truncated opcode 2 word", []byte{'A', 0, 0x00, 0x80, 0x42}, true, "A"},
		{"truncated opcode 3 correction", []byte{'A', 0, 0x00, 0xC0, 0x80}, true, "A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeName(tt.field, tt.unicode); got != tt.want {
				t.Errorf("decodeName(% X, %v) = %q, want %q", tt.field, tt.unicode, got, tt.want)
			}
		})
	}
}

func TestDecodeNameTruncated(t *testing.T) {
	// Every prefix of a valid field must decode without panicking.
	for _, name := range roundTripNames {
		b, _ := encodeName(name)
		for i := range len(b) + 1 {
			decodeName(b[:i], true)
		}
	}
}

func FuzzDecodeName(f *testing.F) {
	for _, name := range roundTripNames {
		b, unicode := encodeName(name)
		f.Add(b, unicode)
	}
	f.Add([]byte{'a', 0, 0x03, 0xC0, 0x80, 0x30}, true)
	f.Fuzz(func(t *testing.T, field []byte, unicode bool) {
		decodeName(field, unicode)
	})
}

func FuzzNameRoundTrip(f *testing.F) {
	for _, name := range roundTripNames {
		f.Add(name)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 {
			t.Skip("names must be valid UTF-8 without a zero byte")
		}
		b, unicode := encodeName(name)
		if got := decodeName(b, unicode); got != name {
			t.Errorf("decodeName(encodeName(%q)) = %q", name, got)
		}
	})
}

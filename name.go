package rar

import (
	"bytes"
	"unicode/utf16"
)

// Opcodes of the encoded UTF-16 part of a file name, two bits per character.
const (
	nameOpByte     = 0 // one byte: the low byte of the character
	nameOpHighByte = 1 // one byte, combined with the HIGH byte
	nameOpWord     = 2 // two bytes, low byte first
	nameOpCopy     = 3 // a run copied from the ASCII part (decoder only)
)

// encodeName returns the FILE_NAME bytes for name, which uses "\" separators and
// must not contain a zero byte. Non-ASCII names are the UTF-8 bytes, a zero byte,
// then RAR's opcode-encoded UTF-16 (HIGH byte, then a flag byte per 4 chars).
func encodeName(name string) (b []byte, unicode bool) {
	ascii := true
	for i := range len(name) {
		if name[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return []byte(name), false
	}

	b = append([]byte(name), 0x00, 0x00) // zero separator, then HIGH = 0
	units := utf16.Encode([]rune(name))
	for len(units) > 0 {
		n := min(4, len(units))
		flagPos := len(b)
		b = append(b, 0)
		for i, u := range units[:n] {
			if u < 0x100 {
				b = append(b, byte(u))
				continue
			}
			// The first character uses the two most significant bits.
			b[flagPos] |= nameOpWord << (6 - 2*i)
			b = append(b, byte(u), byte(u>>8))
		}
		units = units[n:]
	}
	return b, true
}

// decodeName returns the name in field, the inverse of encodeName plus opcode 3
// (runs copied from the ASCII part). Without the Unicode flag the bytes are
// returned as they are; malformed or truncated UTF-16 data ends decoding early.
func decodeName(field []byte, unicode bool) string {
	if !unicode {
		return string(field)
	}
	sep := bytes.IndexByte(field, 0)
	if sep < 0 {
		return string(field)
	}
	ascii, enc := field[:sep], field[sep+1:]
	if len(enc) == 0 {
		return string(ascii)
	}
	units := decodeUnits(ascii, enc[1:], enc[0])
	if len(units) == 0 {
		return string(ascii)
	}
	return string(utf16.Decode(units))
}

// decodeUnits decodes the groups of the UTF-16 part. The ascii argument is
// the ASCII part of the name, which opcode 3 copies from.
func decodeUnits(ascii, enc []byte, high byte) []uint16 {
	var units []uint16
	for len(enc) > 0 {
		flags := enc[0]
		enc = enc[1:]
		for i := 0; i < 4 && len(enc) > 0; i++ {
			switch flags >> (6 - 2*i) & 3 {
			case nameOpByte:
				units = append(units, uint16(enc[0]))
				enc = enc[1:]
			case nameOpHighByte:
				units = append(units, uint16(high)<<8|uint16(enc[0]))
				enc = enc[1:]
			case nameOpWord:
				if len(enc) < 2 {
					return units
				}
				units = append(units, uint16(enc[0])|uint16(enc[1])<<8)
				enc = enc[2:]
			case nameOpCopy:
				if len(enc) < 1 {
					return units
				}
				l := enc[0]
				enc = enc[1:]
				n := int(l) + 2
				var corr byte
				if l&0x80 != 0 {
					if len(enc) < 1 {
						return units
					}
					corr = enc[0]
					enc = enc[1:]
					n = int(l&0x7F) + 2
				}
				for ; n > 0; n-- {
					// The output position indexes the ASCII part.
					pos := len(units)
					if pos >= len(ascii) {
						return units
					}
					if l&0x80 != 0 {
						units = append(units, uint16(ascii[pos]+corr)|uint16(high)<<8)
					} else {
						units = append(units, uint16(ascii[pos]))
					}
				}
			}
		}
	}
	return units
}

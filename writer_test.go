package rar

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"math"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// modified is 2024-03-05 12:34:56 UTC. DOS time keeps 2s resolution, so this
// value is exact.
var modified = time.Date(2024, 3, 5, 12, 34, 56, 0, time.UTC)

// dosModified is modified as an MS-DOS time, field by field: year 2024-1980=44,
// month 3, day 5, hour 12, minute 34, second 56/2=28.
var dosModified = uint32(44)<<25 | 3<<21 | 5<<16 | 12<<11 | 34<<5 | 28

// goldenMain is the main header of an archive. Its HEAD_CRC, 0x90CF, was
// verified independently.
var goldenMain = []byte{0xCF, 0x90, 0x73, 0x00, 0x00, 0x0D, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

// goldenEnd is the end block. Its HEAD_CRC, 0x3DC4, was verified independently.
var goldenEnd = []byte{0xC4, 0x3D, 0x7B, 0x00, 0x40, 0x07, 0x00}

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// fields concatenates byte slices, so expected bytes can be written one field
// per line.
func fields(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

// setHeadCRC fills HEAD_CRC of a block, the low 16 bits of the CRC32 over the
// rest of the block.
func setHeadCRC(b []byte) {
	binary.LittleEndian.PutUint16(b, uint16(crc32.ChecksumIEEE(b[2:])))
}

// archiveBytes wraps blocks in the marker, main header and end block.
func archiveBytes(blocks ...[]byte) []byte {
	parts := [][]byte{[]byte(rarMarker), goldenMain}
	parts = append(parts, blocks...)
	parts = append(parts, goldenEnd)
	return fields(parts...)
}

// entry is a file block read back from archive bytes.
type entry struct {
	fileName []byte // FILE_NAME as stored, with "\" separators
	hostOS   uint8
	flags    uint16
	attr     uint32
	crc      uint32
	packed   uint64
	unpacked uint64
	data     []byte
}

// parseEntries walks the blocks of a writer-made archive, checking each
// HEAD_CRC, and returns its file entries.
func parseEntries(t *testing.T, b []byte) []entry {
	t.Helper()
	if !bytes.HasPrefix(b, []byte(rarMarker)) {
		t.Fatalf("archive does not start with the marker: % X", b[:min(len(b), 7)])
	}
	b = b[len(rarMarker):]

	var entries []entry
	for {
		if len(b) < 7 {
			t.Fatalf("truncated block header: % X", b)
		}
		typ := b[2]
		size := int(binary.LittleEndian.Uint16(b[5:7]))
		if size < 7 || size > len(b) {
			t.Fatalf("block size %d out of range, %d bytes left", size, len(b))
		}
		head := b[:size]
		if got, want := binary.LittleEndian.Uint16(head), uint16(crc32.ChecksumIEEE(head[2:])); got != want {
			t.Fatalf("HEAD_CRC %04X, want %04X for block type %02X", got, want, typ)
		}
		b = b[size:]

		switch typ {
		case mainHeaderType:
		case endBlockType:
			if len(b) != 0 {
				t.Fatalf("%d bytes after the end block", len(b))
			}
			return entries
		case fileHeaderType:
			e := entry{flags: binary.LittleEndian.Uint16(head[3:5])}
			f := head[7:]
			e.packed = uint64(binary.LittleEndian.Uint32(f[0:4]))
			e.unpacked = uint64(binary.LittleEndian.Uint32(f[4:8]))
			e.hostOS = f[8]
			e.crc = binary.LittleEndian.Uint32(f[9:13])
			nameSize := int(binary.LittleEndian.Uint16(f[19:21]))
			e.attr = binary.LittleEndian.Uint32(f[21:25])
			f = f[25:]
			if e.flags&fileFlagLarge != 0 {
				e.packed |= uint64(binary.LittleEndian.Uint32(f[0:4])) << 32
				e.unpacked |= uint64(binary.LittleEndian.Uint32(f[4:8])) << 32
				f = f[8:]
			}
			e.fileName = f[:nameSize]
			if uint64(len(b)) < e.packed {
				t.Fatalf("data of %q is truncated", e.fileName)
			}
			e.data = b[:e.packed]
			b = b[e.packed:]
			entries = append(entries, e)
		default:
			t.Fatalf("unexpected block type %02X", typ)
		}
	}
}

// writeEntries writes one entry per header with data, and returns the archive.
func writeEntries(t *testing.T, headers []*FileHeader, data [][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for i, fh := range headers {
		fw, err := w.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWriterEmptyArchive(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00, // marker
		0xCF, 0x90, 0x73, 0x00, 0x00, 0x0D, 0x00, // main header: HEAD_CRC, type, flags, size
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // RESERVED1 and RESERVED2
		0xC4, 0x3D, 0x7B, 0x00, 0x40, 0x07, 0x00, // end block
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("empty archive = % X, want % X", buf.Bytes(), want)
	}
}

func TestWriterOneFile(t *testing.T) {
	archive := writeEntries(t,
		[]*FileHeader{{Name: "a.txt", Modified: modified}},
		[][]byte{[]byte("hi")},
	)

	head := fields(
		[]byte{0x00, 0x00},                     // HEAD_CRC, set below
		[]byte{0x74},                           // HEAD_TYPE: file header
		le16(0x8000),                           // HEAD_FLAGS: ADD_SIZE
		le16(37),                               // HEAD_SIZE: 32 + len("a.txt")
		le32(2),                                // PACK_SIZE
		le32(2),                                // UNP_SIZE
		[]byte{0x02},                           // HOST_OS: Win32
		le32(crc32.ChecksumIEEE([]byte("hi"))), // FILE_CRC
		le32(dosModified),                      // FTIME
		[]byte{0x1D},                           // UNP_VER: 29
		[]byte{0x30},                           // METHOD: store
		le16(5),                                // NAME_SIZE
		le32(0x20),                             // ATTR: archive
		[]byte("a.txt"),                        // FILE_NAME
	)
	setHeadCRC(head)
	want := archiveBytes(head, []byte("hi"))
	if !bytes.Equal(archive, want) {
		t.Fatalf("archive = % X\nwant      % X", archive, want)
	}
}

func TestWriterDirectory(t *testing.T) {
	archive := writeEntries(t, []*FileHeader{{Name: "d/", Modified: modified}}, [][]byte{nil})

	head := fields(
		[]byte{0x00, 0x00}, // HEAD_CRC, set below
		[]byte{0x74},       // HEAD_TYPE: file header
		le16(0x80E0),       // HEAD_FLAGS: ADD_SIZE and directory
		le16(33),           // HEAD_SIZE: 32 + len("d")
		le32(0),            // PACK_SIZE
		le32(0),            // UNP_SIZE
		[]byte{0x02},       // HOST_OS: Win32
		le32(0),            // FILE_CRC
		le32(dosModified),  // FTIME
		[]byte{0x1D},       // UNP_VER: 29
		[]byte{0x30},       // METHOD: store
		le16(1),            // NAME_SIZE: no trailing separator
		le32(0x10),         // ATTR: directory
		[]byte("d"),        // FILE_NAME
	)
	setHeadCRC(head)
	if want := archiveBytes(head); !bytes.Equal(archive, want) {
		t.Fatalf("archive = % X\nwant      % X", archive, want)
	}
}

func TestWriterDefaultAttributes(t *testing.T) {
	headers := []*FileHeader{
		{Name: "f.txt"},
		{Name: "d/"},
		{Name: "unix", HostOS: HostUnix, Attributes: 0o100444},
		{Name: "win-ro", HostOS: HostWin32, Attributes: attrArchive | attrReadOnly},
	}
	archive := writeEntries(t, headers, make([][]byte, len(headers)))

	entries := parseEntries(t, archive)
	if len(entries) != len(headers) {
		t.Fatalf("got %d entries, want %d", len(entries), len(headers))
	}
	wantAttr := []uint32{attrArchive, attrDir, attrArchive | attrReadOnly, attrArchive | attrReadOnly}
	for i, e := range entries {
		if e.attr != wantAttr[i] {
			t.Errorf("entry %d: ATTR %#x, want %#x", i, e.attr, wantAttr[i])
		}
		if e.hostOS != HostWin32 {
			t.Errorf("entry %d: HOST_OS %d, want %d", i, e.hostOS, HostWin32)
		}
	}
	if entries[1].flags&fileFlagDir != fileFlagDir {
		t.Errorf("directory flags %#x have no directory bits", entries[1].flags)
	}
}

func TestFileHeaderLarge(t *testing.T) {
	// Only the header is built, so no 5 GiB of data is written.
	const size = 5 << 30
	name := []byte("big.bin")
	fh := &FileHeader{
		Modified:     modified,
		HostOS:       HostWin32,
		Attributes:   attrArchive,
		Method:       Store,
		PackedSize:   size,
		UnpackedSize: size,
	}
	head := fields(
		[]byte{0x00, 0x00},  // HEAD_CRC, set below
		[]byte{0x74},        // HEAD_TYPE
		le16(0x8000|0x0100), // HEAD_FLAGS: ADD_SIZE and LARGE
		le16(32+8+7),        // HEAD_SIZE
		le32(0x40000000),    // PACK_SIZE: low 32 bits of 5 GiB
		le32(0x40000000),    // UNP_SIZE: low 32 bits of 5 GiB
		[]byte{0x02},        // HOST_OS
		le32(0),             // FILE_CRC
		le32(dosModified),   // FTIME
		[]byte{0x1D},        // UNP_VER
		[]byte{0x30},        // METHOD
		le16(7),             // NAME_SIZE
		le32(0x20),          // ATTR
		le32(1),             // HIGH_PACK_SIZE: high 32 bits of 5 GiB
		le32(1),             // HIGH_UNP_SIZE
		name,                // FILE_NAME
	)
	setHeadCRC(head)
	if got := fileHeaderBytes(fh, name); !bytes.Equal(got, head) {
		t.Fatalf("header = % X\nwant     % X", got, head)
	}
}

func TestFileHeaderLargeLimit(t *testing.T) {
	// 0xFFFFFFFF still fits in 32 bits, so LARGE stays clear.
	fh := &FileHeader{HostOS: HostWin32, Modified: modified, PackedSize: math.MaxUint32, UnpackedSize: math.MaxUint32}
	b := fileHeaderBytes(fh, []byte("a"))
	if flags := binary.LittleEndian.Uint16(b[3:5]); flags&fileFlagLarge != 0 {
		t.Fatalf("flags %#x have LARGE for a 0xFFFFFFFF size", flags)
	}
	if got := binary.LittleEndian.Uint16(b[5:7]); got != 33 {
		t.Fatalf("HEAD_SIZE = %d, want 33", got)
	}
}

func TestWriterInvalidNames(t *testing.T) {
	for _, name := range []string{"", "/", "//", "a\x00b", "\xff.txt", "dir/\x00/x", `back\slash.txt`, `dir\`} {
		w := NewWriter(io.Discard)
		if _, err := w.Create(name); err == nil {
			t.Errorf("Create(%q) succeeded, want an error", name)
		}
	}
	w := NewWriter(io.Discard)
	if _, err := w.CreateHeader(&FileHeader{Name: "/", Modified: modified}); err == nil {
		t.Error("a directory named \"/\" was accepted")
	}
}

func TestWriterNameStored(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"/abs/a.txt", `abs\a.txt`},
		{"dir/sub/a.txt", `dir\sub\a.txt`},
		{"dir/", "dir"},
		{"/dir/", "dir"},
	}
	for _, tt := range tests {
		entries := parseEntries(t, writeEntries(t, []*FileHeader{{Name: tt.name, Modified: modified}}, [][]byte{nil}))
		if len(entries) != 1 {
			t.Fatalf("CreateHeader(%q): got %d entries, want 1", tt.name, len(entries))
		}
		if string(entries[0].fileName) != tt.want {
			t.Errorf("CreateHeader(%q) stored %q, want %q", tt.name, entries[0].fileName, tt.want)
		}
	}
}

func TestWriterErrors(t *testing.T) {
	t.Run("algorithm", func(t *testing.T) {
		w := NewWriter(io.Discard)
		if _, err := w.CreateHeader(&FileHeader{Name: "a", Method: 0x31}); !errors.Is(err, ErrAlgorithm) {
			t.Errorf("error = %v, want ErrAlgorithm", err)
		}
		if _, err := w.CreateHeader(&FileHeader{Name: "a", Method: Store}); err != nil {
			t.Errorf("Store method: %v", err)
		}
	})

	t.Run("write to directory", func(t *testing.T) {
		w := NewWriter(io.Discard)
		d, err := w.Create("d/")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Write(nil); err != nil {
			t.Errorf("empty write to a directory: %v", err)
		}
		if _, err := d.Write([]byte("x")); !errors.Is(err, errDirWrite) {
			t.Errorf("write to a directory: error = %v, want errDirWrite", err)
		}
	})

	t.Run("stale entry", func(t *testing.T) {
		w := NewWriter(io.Discard)
		a, err := w.Create("a")
		if err != nil {
			t.Fatal(err)
		}
		b, err := w.Create("b")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Write([]byte("x")); !errors.Is(err, errEntryClosed) {
			t.Errorf("write to stale entry: error = %v, want errEntryClosed", err)
		}
		if _, err := b.Write([]byte("x")); err != nil {
			t.Errorf("write to current entry: %v", err)
		}
	})

	t.Run("after close", func(t *testing.T) {
		w := NewWriter(io.Discard)
		f, err := w.Create("a")
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("x")); err == nil {
			t.Error("write after Close succeeded")
		}
		if _, err := w.Create("b"); !errors.Is(err, errWriterClosed) {
			t.Errorf("Create after Close: error = %v, want errWriterClosed", err)
		}
		if err := w.Close(); !errors.Is(err, errWriterClosed) {
			t.Errorf("second Close: error = %v, want errWriterClosed", err)
		}
	})
}

// closeRecorder records whether Close was called on it.
type closeRecorder struct {
	bytes.Buffer
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

func TestWriterCloseKeepsUnderlying(t *testing.T) {
	var c closeRecorder
	if err := NewWriter(&c).Close(); err != nil {
		t.Fatal(err)
	}
	if c.closed {
		t.Error("Close closed the underlying writer")
	}
}

func TestWriterUnicodeName(t *testing.T) {
	const name = "Ñandú.txt"
	entries := parseEntries(t, writeEntries(t, []*FileHeader{{Name: name, Modified: modified}}, [][]byte{nil}))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.flags&fileFlagUnicode == 0 {
		t.Errorf("flags %#x do not have UNICODE", e.flags)
	}
	want, _ := encodeName(name)
	if !bytes.Equal(e.fileName, want) {
		t.Errorf("FILE_NAME = % X, want % X", e.fileName, want)
	}
}

func TestWriterIgnoresCallerCRCAndSize(t *testing.T) {
	fh := &FileHeader{
		Name:         "a.txt",
		Modified:     modified,
		CRC32:        0xDEADBEEF,
		PackedSize:   999,
		UnpackedSize: 999,
		Flags:        0x1234,
	}
	entries := parseEntries(t, writeEntries(t, []*FileHeader{fh}, [][]byte{[]byte("hi")}))
	e := entries[0]
	if e.crc != crc32.ChecksumIEEE([]byte("hi")) || e.packed != 2 || e.unpacked != 2 {
		t.Errorf("crc %08X packed %d unpacked %d, want the computed values", e.crc, e.packed, e.unpacked)
	}
	if e.flags != 0x8000 {
		t.Errorf("flags %#x, want 0x8000", e.flags)
	}
	if fh.CRC32 != 0xDEADBEEF {
		t.Error("CreateHeader changed the caller's header")
	}
}

func TestWriterHostOSConversion(t *testing.T) {
	// A Unix header is written as Win32, with attributes converted to match.
	fh := &FileHeader{Name: "u.txt", Modified: modified, HostOS: HostUnix, Attributes: 0o100444}
	e := parseEntries(t, writeEntries(t, []*FileHeader{fh}, [][]byte{nil}))[0]
	if e.hostOS != HostWin32 {
		t.Errorf("HOST_OS = %d, want %d", e.hostOS, HostWin32)
	}
	if want := uint32(attrArchive | attrReadOnly); e.attr != want {
		t.Errorf("ATTR = %#x, want %#x", e.attr, want)
	}
}

func TestWriterAddFS(t *testing.T) {
	fsys := fstest.MapFS{
		"top.txt":   {Data: []byte("top")},
		"a/b.txt":   {Data: []byte("bee")},
		"a/c/d.txt": {Data: []byte("dee")},
	}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.AddFS(fsys); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Lexical order: "a" sorts before "top.txt".
	entries := parseEntries(t, buf.Bytes())
	wantNames := []string{`a`, `a\b.txt`, `a\c`, `a\c\d.txt`, `top.txt`}
	wantData := []string{"", "bee", "", "dee", "top"}
	if len(entries) != len(wantNames) {
		t.Fatalf("got %d entries, want %d", len(entries), len(wantNames))
	}
	for i, e := range entries {
		if string(e.fileName) != wantNames[i] {
			t.Errorf("entry %d name = %q, want %q", i, e.fileName, wantNames[i])
		}
		if string(e.data) != wantData[i] {
			t.Errorf("entry %d data = %q, want %q", i, e.data, wantData[i])
		}
		isDir := e.flags&fileFlagDir == fileFlagDir
		if wantDir := wantData[i] == ""; isDir != wantDir {
			t.Errorf("entry %d directory = %v, want %v", i, isDir, wantDir)
		}
	}
}

func TestWriterAddFSUnsupportedType(t *testing.T) {
	fsys := fstest.MapFS{
		"link": {Data: []byte("target"), Mode: fs.ModeSymlink},
	}
	w := NewWriter(io.Discard)
	if err := w.AddFS(fsys); err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Errorf("AddFS with a symlink: error = %v, want an unsupported file type error", err)
	}
}

func TestWriterAttributes(t *testing.T) {
	tests := []struct {
		name string
		fh   FileHeader
		want uint32
	}{
		{"zero header", FileHeader{Name: "a"}, attrArchive},
		{"zero attributes on unix host", FileHeader{Name: "a", HostOS: HostUnix}, attrArchive},
		{"zero attributes on unix dir", FileHeader{Name: "d/", HostOS: HostUnix}, attrDir},
		{"unix mode converted", FileHeader{Name: "a", HostOS: HostUnix, Attributes: 0o100444}, attrArchive | attrReadOnly},
		{"win32 hidden and system kept", FileHeader{Name: "a", HostOS: HostWin32, Attributes: 0x02 | 0x04 | attrArchive}, 0x02 | 0x04 | attrArchive},
		{"dos attributes kept", FileHeader{Name: "a", HostOS: HostMSDOS, Attributes: 0x02 | attrArchive}, 0x02 | attrArchive},
		{"win32 dir bit follows name", FileHeader{Name: "d/", HostOS: HostWin32, Attributes: attrArchive}, attrArchive | attrDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := parseEntries(t, writeEntries(t, []*FileHeader{&tt.fh}, [][]byte{nil}))[0]
			if e.hostOS != HostWin32 {
				t.Errorf("HOST_OS = %d, want %d", e.hostOS, HostWin32)
			}
			if e.attr != tt.want {
				t.Errorf("ATTR = %#x, want %#x", e.attr, tt.want)
			}
		})
	}
}

func TestWriterNameLimit(t *testing.T) {
	// With m characters, s of them ASCII and the rest U+00E9, the encoded name
	// is 3m-s+2+ceil(m/4) bytes, so s picks the exact length.
	const m = 21000
	name := func(s int) string {
		return strings.Repeat("a", s) + strings.Repeat("é", m-s)
	}
	ok, tooLong := name(2757), name(2756)
	if enc, _ := encodeName(ok); len(enc) != 65495 {
		t.Fatalf("test name encodes to %d bytes, want 65495", len(enc))
	}
	if enc, _ := encodeName(tooLong); len(enc) != 65496 {
		t.Fatalf("test name encodes to %d bytes, want 65496", len(enc))
	}

	w := NewWriter(io.Discard)
	if _, err := w.Create(ok); err != nil {
		t.Errorf("name of 65495 encoded bytes: %v", err)
	}
	w = NewWriter(io.Discard)
	if _, err := w.Create(tooLong); err == nil {
		t.Error("name of 65496 encoded bytes was accepted")
	}
}

// failWriter accepts limit bytes, then fails.
type failWriter struct {
	limit int
}

var errDiskFull = errors.New("disk full")

func (f *failWriter) Write(p []byte) (int, error) {
	if len(p) > f.limit {
		n := f.limit
		f.limit = 0
		return n, errDiskFull
	}
	f.limit -= len(p)
	return len(p), nil
}

func TestWriterStickyError(t *testing.T) {
	// Whichever call hits the failure, every later call must report an error.
	for limit := range 200 {
		w := NewWriter(&failWriter{limit: limit})
		var errs []error
		for _, name := range []string{"a.txt", "b.txt"} {
			f, err := w.Create(name)
			errs = append(errs, err)
			if err == nil {
				_, err = f.Write([]byte("hello"))
				errs = append(errs, err)
			}
		}
		errs = append(errs, w.Close())

		failed := false
		for i, err := range errs {
			if failed && err == nil {
				t.Fatalf("limit %d: call %d succeeded after an earlier error", limit, i)
			}
			if err != nil {
				failed = true
			}
		}
	}

	w := NewWriter(&failWriter{limit: 0})
	if err := w.Close(); !errors.Is(err, errDiskFull) {
		t.Errorf("Close on a failing writer: error = %v, want errDiskFull", err)
	}
}

package rar

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// firstFileOff is the offset of the first block after the marker and the main
// header, which is where the tests find the first file header.
const firstFileOff = len(rarMarker) + 13

// fixHeadCRC recomputes HEAD_CRC of the block at off, after a test changed it.
func fixHeadCRC(b []byte, off int) {
	size := int(binary.LittleEndian.Uint16(b[off+5:]))
	setHeadCRC(b[off : off+size])
}

// fileBlock returns a file header block for name, followed by data. The name is
// stored as given, so tests can use "\" or "..".
func fileBlock(hostOS uint8, flags uint16, attr uint32, name string, data []byte) []byte {
	fh := &FileHeader{
		HostOS:       hostOS,
		Flags:        flags,
		Attributes:   attr,
		Modified:     modified,
		CRC32:        crc32.ChecksumIEEE(data),
		PackedSize:   uint64(len(data)),
		UnpackedSize: uint64(len(data)),
	}
	return fields(fileHeaderBytes(fh, []byte(name)), data)
}

// readAllFile opens f and reads it to EOF, returning the data and any error.
func readAllFile(f *File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// readerOf returns a Reader over b.
func readerOf(t *testing.T, b []byte) *Reader {
	t.Helper()
	rr, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return rr
}

func TestReaderRoundTrip(t *testing.T) {
	big := make([]byte, 1<<20)
	rng := rand.NewChaCha8([32]byte{'r', 'e', 'a', 'd'})
	_, _ = rng.Read(big)

	ro := &FileHeader{Name: "ro.txt", Modified: modified}
	ro.SetMode(0444)
	cases := []struct {
		fh   *FileHeader
		data []byte
		mode fs.FileMode
	}{
		{&FileHeader{Name: "docs/", Modified: modified}, nil, fs.ModeDir | 0777},
		{&FileHeader{Name: "docs/readme.txt", Modified: modified}, []byte("hello"), 0666},
		{&FileHeader{Name: "Ñandú/ページ 01.jpg", Modified: modified}, []byte("\xff\xd8\xff"), 0666},
		{&FileHeader{Name: "😀.png", Modified: modified}, []byte("png"), 0666},
		{&FileHeader{Name: "empty.txt", Modified: modified}, nil, 0666},
		{&FileHeader{Name: "big.bin", Modified: modified}, big, 0666},
		{ro, []byte("read only"), 0444},
		// DOS time keeps 2s, so the odd second is dropped on the way back.
		{&FileHeader{Name: "odd.txt", Modified: modified.Add(time.Second)}, []byte("x"), 0666},
	}
	var headers []*FileHeader
	var data [][]byte
	for _, c := range cases {
		headers = append(headers, c.fh)
		data = append(data, c.data)
	}
	rr := readerOf(t, writeEntries(t, headers, data))

	if len(rr.File) != len(cases) {
		t.Fatalf("got %d files, want %d", len(rr.File), len(cases))
	}
	for i, c := range cases {
		f := rr.File[i]
		if f.Name != c.fh.Name {
			t.Errorf("name %q, want %q", f.Name, c.fh.Name)
		}
		if !f.Modified.Equal(modified) {
			t.Errorf("%s: Modified %v, want %v", f.Name, f.Modified, modified)
		}
		if f.Mode() != c.mode {
			t.Errorf("%s: Mode %v, want %v", f.Name, f.Mode(), c.mode)
		}
		if f.UnpackedSize != uint64(len(c.data)) || f.PackedSize != uint64(len(c.data)) {
			t.Errorf("%s: sizes %d/%d, want %d", f.Name, f.PackedSize, f.UnpackedSize, len(c.data))
		}
		if f.CRC32 != crc32.ChecksumIEEE(c.data) {
			t.Errorf("%s: CRC32 %08X", f.Name, f.CRC32)
		}
		got, err := readAllFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if !bytes.Equal(got, c.data) {
			t.Errorf("%s: contents differ, %d bytes read, want %d", f.Name, len(got), len(c.data))
		}
	}
}

func TestReaderFS(t *testing.T) {
	headers := []*FileHeader{
		{Name: "top.txt", Modified: modified},
		// No "a/", "a/b/" or "x/" entries: the reader must synthesize them.
		{Name: "a/b/c.txt", Modified: modified},
		{Name: "a/b/d/", Modified: modified},
		{Name: "Ñandú/ページ 01.jpg", Modified: modified},
		{Name: "x/y/z.txt", Modified: modified},
	}
	data := [][]byte{[]byte("top"), []byte("c"), nil, []byte("jpg"), []byte("zed")}
	rr := readerOf(t, writeEntries(t, headers, data))

	expected := []string{
		"top.txt",
		"a", "a/b", "a/b/c.txt", "a/b/d",
		"Ñandú", "Ñandú/ページ 01.jpg",
		"x", "x/y", "x/y/z.txt",
	}
	if err := fstest.TestFS(rr, expected...); err != nil {
		t.Fatal(err)
	}

	info, err := fs.Stat(rr, "a/b")
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Errorf("a/b is not a directory: %v", info.Mode())
	}
	if _, err := rr.Open("nope.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(nope.txt) = %v, want ErrNotExist", err)
	}
	if _, err := rr.Open("../top.txt"); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("Open(../top.txt) = %v, want ErrInvalid", err)
	}
}

func TestReaderErrors(t *testing.T) {
	base := writeEntries(t, []*FileHeader{{Name: "a.txt", Modified: modified}}, [][]byte{[]byte("hello")})
	dataOff := firstFileOff + int(binary.LittleEndian.Uint16(base[firstFileOff+5:]))

	tests := []struct {
		name    string
		mutate  func(b []byte)
		newErr  error // NewReader error
		openErr error // File.Open error
		readErr error // error reading to EOF
	}{
		{name: "bad marker", mutate: func(b []byte) { b[0] = 'X' }, newErr: ErrFormat},
		{name: "RAR5 marker", mutate: func(b []byte) { copy(b, "Rar!\x1a\x07\x01\x00") }, newErr: ErrUnsupported},
		{name: "corrupted header", mutate: func(b []byte) { b[firstFileOff+10] ^= 0xFF }, newErr: ErrFormat},
		{name: "HEAD_SIZE below 7", mutate: func(b []byte) {
			binary.LittleEndian.PutUint16(b[firstFileOff+5:], 6)
			fixHeadCRC(b, firstFileOff)
		}, newErr: ErrFormat},
		// The size check comes before the CRC, so the CRC is left stale.
		{name: "HEAD_SIZE past the end", mutate: func(b []byte) {
			binary.LittleEndian.PutUint16(b[firstFileOff+5:], 0xFFFF)
		}, newErr: ErrFormat},
		{name: "main volume flag", mutate: func(b []byte) {
			b[len(rarMarker)+3] |= mainFlagVolume
			fixHeadCRC(b, len(rarMarker))
		}, newErr: ErrUnsupported},
		{name: "main encrypted headers flag", mutate: func(b []byte) {
			b[len(rarMarker)+3] |= mainFlagEncHeader
			fixHeadCRC(b, len(rarMarker))
		}, newErr: ErrUnsupported},
		{name: "method 0x33", mutate: func(b []byte) {
			b[firstFileOff+7+18] = 0x33
			fixHeadCRC(b, firstFileOff)
		}, openErr: ErrAlgorithm},
		{name: "encrypted", mutate: func(b []byte) {
			b[firstFileOff+3] |= fileFlagEncrypted
			fixHeadCRC(b, firstFileOff)
		}, openErr: ErrUnsupported},
		{name: "split before", mutate: func(b []byte) {
			b[firstFileOff+3] |= fileFlagSplitBefore
			fixHeadCRC(b, firstFileOff)
		}, openErr: ErrUnsupported},
		{name: "split after", mutate: func(b []byte) {
			b[firstFileOff+3] |= fileFlagSplitAfter
			fixHeadCRC(b, firstFileOff)
		}, openErr: ErrUnsupported},
		{name: "unpacked size too small", mutate: func(b []byte) {
			binary.LittleEndian.PutUint32(b[firstFileOff+7+4:], 4)
			fixHeadCRC(b, firstFileOff)
		}, openErr: ErrFormat},
		{name: "corrupted data", mutate: func(b []byte) { b[dataOff] ^= 0x20 }, readErr: ErrChecksum},
		{name: "corrupted CRC32", mutate: func(b []byte) {
			binary.LittleEndian.PutUint32(b[firstFileOff+7+9:], 0xDEADBEEF)
			fixHeadCRC(b, firstFileOff)
		}, readErr: ErrChecksum},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := bytes.Clone(base)
			tc.mutate(b)
			rr, err := NewReader(bytes.NewReader(b), int64(len(b)))
			if !errors.Is(err, tc.newErr) {
				t.Fatalf("NewReader: %v, want %v", err, tc.newErr)
			}
			if tc.newErr != nil {
				return
			}
			_, err = rr.File[0].Open()
			if !errors.Is(err, tc.openErr) {
				t.Fatalf("Open: %v, want %v", err, tc.openErr)
			}
			if tc.openErr != nil {
				return
			}
			_, err = readAllFile(rr.File[0])
			if !errors.Is(err, tc.readErr) {
				t.Fatalf("read: %v, want %v", err, tc.readErr)
			}
		})
	}
}

func TestReaderHugeSizeIsRejected(t *testing.T) {
	// A LARGE header declaring 4 GiB of data in an archive of a few bytes must
	// fail on the bounds check, not on an allocation.
	fh := &FileHeader{HostOS: HostWin32, Attributes: attrArchive, Modified: modified,
		PackedSize: 1<<32 + 5, UnpackedSize: 5}
	b := archiveBytes(fileHeaderBytes(fh, []byte("big.bin")), []byte("hello"))
	if _, err := NewReader(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrFormat) {
		t.Fatalf("NewReader: %v, want ErrFormat", err)
	}
}

func TestReaderHugeUnpackedSize(t *testing.T) {
	// A stored file whose UnpackedSize is far beyond its data must not be
	// readable: fs.ReadFile would allocate the size that Stat reports.
	fh := &FileHeader{HostOS: HostWin32, Attributes: attrArchive, Modified: modified,
		PackedSize: 5, UnpackedSize: 1 << 40}
	b := archiveBytes(fileHeaderBytes(fh, []byte("big.bin")), []byte("hello"))
	rr := readerOf(t, b)
	if _, err := fs.ReadFile(rr, "big.bin"); !errors.Is(err, ErrFormat) {
		t.Fatalf("fs.ReadFile: %v, want ErrFormat", err)
	}
}

func TestReaderTruncated(t *testing.T) {
	b := writeEntries(t,
		[]*FileHeader{{Name: "d/", Modified: modified}, {Name: "d/a.txt", Modified: modified}},
		[][]byte{nil, []byte("some data")})
	dataOff := len(b) - len(goldenEnd) - len("some data")

	// Cutting the end block short, or the data, leaves the archive truncated.
	for n := len(b) - len(goldenEnd) + 1; n < len(b); n++ {
		if _, err := NewReader(bytes.NewReader(b[:n]), int64(n)); !errors.Is(err, ErrFormat) {
			t.Errorf("%d bytes: NewReader = %v, want ErrFormat", n, err)
		}
	}
	if _, err := NewReader(bytes.NewReader(b[:dataOff+3]), int64(dataOff+3)); !errors.Is(err, ErrFormat) {
		t.Errorf("cut in data: NewReader = %v, want ErrFormat", err)
	}

	// Every prefix must be handled without a panic. A prefix that ends at a
	// block boundary is a valid archive without an end block.
	for n := range b {
		rr, err := NewReader(bytes.NewReader(b[:n]), int64(n))
		if err != nil {
			continue
		}
		for _, f := range rr.File {
			_, _ = readAllFile(f)
		}
	}
}

func TestReaderInsecurePath(t *testing.T) {
	tests := []struct {
		name string
		fb   []byte
	}{
		{"parent", fileBlock(HostWin32, 0, attrArchive, "../evil", []byte("x"))},
		{"drive on win32", fileBlock(HostWin32, 0, attrArchive, `C:\x`, []byte("x"))},
		{"drive on unix", fileBlock(HostUnix, 0, 0o100644, `C:\x`, []byte("x"))},
		{"absolute", fileBlock(HostUnix, 0, 0o100644, "/abs", []byte("x"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := archiveBytes(tc.fb, fileBlock(HostWin32, 0, attrArchive, "ok.txt", []byte("ok")))
			rr, err := NewReader(bytes.NewReader(b), int64(len(b)))
			if !errors.Is(err, ErrInsecurePath) {
				t.Fatalf("NewReader: %v, want ErrInsecurePath", err)
			}
			if rr == nil || len(rr.File) != 2 {
				t.Fatal("NewReader did not return the reader")
			}
			if got, err := readAllFile(rr.File[0]); err != nil || string(got) != "x" {
				t.Errorf("insecure entry: %q, %v", got, err)
			}
			if got, err := fs.ReadFile(rr, "ok.txt"); err != nil || string(got) != "ok" {
				t.Errorf("ok.txt: %q, %v", got, err)
			}
		})
	}
}

func TestReaderBackslashNames(t *testing.T) {
	b := archiveBytes(fileBlock(HostWin32, 0, attrArchive, `sub\file.txt`, []byte("x")),
		fileBlock(HostWin32, fileFlagDir, attrDir, `sub\dir`, nil))
	rr := readerOf(t, b)
	if rr.File[0].Name != "sub/file.txt" || rr.File[1].Name != "sub/dir/" {
		t.Errorf("names %q, %q", rr.File[0].Name, rr.File[1].Name)
	}
}

func TestReaderSkipsUnknownBlocks(t *testing.T) {
	// A block with no ADD_SIZE, and one whose ADD_SIZE data must be skipped too.
	unknown := finishBlock(append(baseHeader(0x78, 0, 9), 1, 2))
	withAdd := finishBlock(append(baseHeader(0x79, blockFlagAddSize, 11), le32(3)...))
	withAdd = fields(withAdd, []byte("abc"))

	b := archiveBytes(
		fileBlock(HostWin32, 0, attrArchive, "a.txt", []byte("aa")),
		unknown,
		withAdd,
		fileBlock(HostWin32, 0, attrArchive, "b.txt", []byte("bb")),
	)
	rr := readerOf(t, b)
	if len(rr.File) != 2 {
		t.Fatalf("got %d files, want 2", len(rr.File))
	}
	for i, want := range []string{"aa", "bb"} {
		got, err := readAllFile(rr.File[i])
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", rr.File[i].Name, got, err, want)
		}
	}
}

func TestReaderIgnoresTrailingHeaderBytes(t *testing.T) {
	// Salt and extended time follow the name inside HEAD_SIZE and are ignored.
	blk := fileHeaderBytes(&FileHeader{HostOS: HostWin32, Attributes: attrArchive, Modified: modified,
		PackedSize: 2, UnpackedSize: 2, CRC32: crc32.ChecksumIEEE([]byte("hi"))}, []byte("a.txt"))
	blk = append(blk, 1, 2, 3, 4, 5, 6, 7, 8)
	binary.LittleEndian.PutUint16(blk[5:], uint16(len(blk)))
	setHeadCRC(blk)

	rr := readerOf(t, archiveBytes(fields(blk, []byte("hi"))))
	if rr.File[0].Name != "a.txt" {
		t.Errorf("name %q", rr.File[0].Name)
	}
	if got, err := readAllFile(rr.File[0]); err != nil || string(got) != "hi" {
		t.Errorf("data %q, %v", got, err)
	}
}

func TestReaderUnixMode(t *testing.T) {
	tests := []struct {
		name string
		fb   []byte
		want fs.FileMode
		dir  bool
	}{
		{"bin/tool", fileBlock(HostUnix, 0, 0o100750, "bin/tool", []byte("x")), 0750, false},
		{"link", fileBlock(HostUnix, 0, 0o120777, "link", []byte("tool")), fs.ModeSymlink | 0777, false},
		{"dir", fileBlock(HostUnix, fileFlagDir, 0o40755, "dir", nil), fs.ModeDir | 0755, true},
	}
	var blocks [][]byte
	for _, tc := range tests {
		blocks = append(blocks, tc.fb)
	}
	rr := readerOf(t, archiveBytes(blocks...))
	for i, tc := range tests {
		f := rr.File[i]
		if f.HostOS != HostUnix {
			t.Errorf("%s: HostOS %d, want %d", tc.name, f.HostOS, HostUnix)
		}
		if f.Mode() != tc.want {
			t.Errorf("%s: Mode %v, want %v", tc.name, f.Mode(), tc.want)
		}
		if f.Mode().IsDir() != tc.dir {
			t.Errorf("%s: IsDir %v, want %v", tc.name, f.Mode().IsDir(), tc.dir)
		}
	}
}

func TestReaderDirectoryOpen(t *testing.T) {
	rr := readerOf(t, writeEntries(t, []*FileHeader{{Name: "d/", Modified: modified}}, [][]byte{nil}))
	f := rr.File[0]
	if got, err := readAllFile(f); err != nil || len(got) != 0 {
		t.Errorf("directory read %q, %v", got, err)
	}
	dir, err := rr.Open("d")
	if err != nil {
		t.Fatal(err)
	}
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		t.Errorf("Stat = %v, %v", info, err)
	}
}

func TestReaderDataOffset(t *testing.T) {
	rr := readerOf(t, writeEntries(t, []*FileHeader{{Name: "a.txt", Modified: modified}}, [][]byte{[]byte("hello")}))
	off, err := rr.File[0].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	// The header is 32 bytes plus the 5-byte name.
	if want := int64(firstFileOff + 32 + len("a.txt")); off != want {
		t.Errorf("DataOffset = %d, want %d", off, want)
	}
}

func TestReaderLibarchiveFixtures(t *testing.T) {
	type entry struct {
		name   string
		size   uint64
		method uint8
		mtime  time.Time
		mode   fs.FileMode
		crc    uint32 // zero when the entry is not read in full
	}
	dos := func(y int, m time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, m, d, h, mi, s, 0, time.UTC)
	}
	const store, compressed = Store, 0x33

	tests := []struct {
		file    string
		entries []entry
	}{
		{"test_read_format_rar.rar", []entry{
			{"test.txt", 20, store, dos(2011, 6, 26, 14, 53, 46), 0644, 0xBEC8A242},
			{"testlink", 8, store, dos(2011, 6, 24, 9, 38, 34), fs.ModeSymlink | 0777, 0xB6C9447B},
			{"testdir/test.txt", 20, store, dos(2011, 6, 26, 14, 59, 6), 0644, 0xBEC8A242},
			{"testdir/", 0, store, dos(2011, 6, 26, 14, 59, 6), fs.ModeDir | 0755, 0},
			{"testemptydir/", 0, store, dos(2011, 6, 21, 21, 28, 58), fs.ModeDir | 0755, 0},
		}},
		{"test_read_format_rar_unicode.rar", []entry{
			{"表だよ/新しいフォルダ/新規テキスト ドキュメント.txt", 0, store, dos(2011, 7, 1, 13, 9, 4), 0666, 0},
			{"表だよ/漢字長いファイル名long-filename-in-漢字.txt", 5, store, dos(2011, 3, 20, 10, 37, 44), 0666, 0x426F9DDC},
			{"表だよ/新しいフォルダ/", 0, store, dos(2011, 7, 1, 13, 9, 4), fs.ModeDir | 0777, 0},
			{"表だよ/", 0, store, dos(2011, 7, 1, 13, 8, 30), fs.ModeDir | 0777, 0},
			{"表だよ/ファイル", 54, store, dos(2011, 8, 11, 10, 34, 48), fs.ModeSymlink | 0755, 0x5DFB8225},
			{"abcdefghijklmnopqrsテスト.txt", 16, compressed, dos(2012, 3, 7, 16, 38, 58), 0664, 0},
		}},
		{"test_read_format_rar_windows.rar", []entry{
			{"testdir/test.txt", 16, store, dos(2011, 8, 18, 12, 11, 24), 0666, 0x97D612F1},
			{"test.txt", 16, store, dos(2011, 8, 18, 12, 11, 24), 0666, 0x97D612F1},
			{"testshortcut.lnk", 441, store, dos(2011, 8, 18, 12, 14, 14), 0666, 0x5B5CD737},
			{"testdir/", 0, store, dos(2011, 8, 18, 12, 11, 42), fs.ModeDir | 0777, 0},
			{"testemptydir/", 0, store, dos(2011, 8, 18, 12, 11, 50), fs.ModeDir | 0777, 0},
		}},
		{"test_read_format_rar_subblock.rar", []entry{
			{"test.txt", 20, store, dos(2011, 6, 26, 14, 53, 46), 0644, 0xBEC8A242},
		}},
		{"test_read_format_rar_noeof.rar", []entry{
			{"test.txt", 20, store, dos(2011, 6, 26, 14, 53, 46), 0644, 0xBEC8A242},
		}},
		{"test_read_format_rar_compress_normal.rar", []entry{
			{"LibarchiveAddingTest.html", 20111, compressed, dos(2011, 6, 26, 22, 25, 44), 0644, 0},
			{"testlink", 25, store, dos(2011, 7, 13, 21, 4, 50), fs.ModeSymlink | 0777, 0x11FCD3F1},
			{"testdir/test.txt", 20, compressed, dos(2011, 6, 26, 14, 59, 6), 0644, 0},
			{"testdir/LibarchiveAddingTest.html", 20111, compressed, dos(2011, 6, 26, 22, 25, 44), 0644, 0},
			{"testdir/", 0, store, dos(2011, 7, 13, 21, 3, 36), fs.ModeDir | 0755, 0},
			{"testemptydir/", 0, store, dos(2011, 6, 21, 21, 28, 58), fs.ModeDir | 0755, 0},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", "libarchive", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			rr := readerOf(t, b)
			if len(rr.File) != len(tc.entries) {
				t.Fatalf("got %d files, want %d", len(rr.File), len(tc.entries))
			}
			for i, want := range tc.entries {
				f := rr.File[i]
				if f.Name != want.name {
					t.Errorf("name %q, want %q", f.Name, want.name)
				}
				if f.UnpackedSize != want.size {
					t.Errorf("%s: size %d, want %d", want.name, f.UnpackedSize, want.size)
				}
				if f.Method != want.method {
					t.Errorf("%s: method %#x, want %#x", want.name, f.Method, want.method)
				}
				if !f.Modified.Equal(want.mtime) {
					t.Errorf("%s: Modified %v, want %v", want.name, f.Modified, want.mtime)
				}
				if f.Mode() != want.mode {
					t.Errorf("%s: Mode %v, want %v", want.name, f.Mode(), want.mode)
				}
				if f.Mode().IsDir() != (want.mode.IsDir()) {
					t.Errorf("%s: IsDir %v", want.name, f.Mode().IsDir())
				}

				got, err := readAllFile(f)
				if want.method != Store {
					if !errors.Is(err, ErrAlgorithm) {
						t.Errorf("%s: read %v, want ErrAlgorithm", want.name, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: read: %v", want.name, err)
					continue
				}
				if uint64(len(got)) != want.size {
					t.Errorf("%s: read %d bytes, want %d", want.name, len(got), want.size)
				}
				if want.crc != 0 && f.CRC32 != want.crc {
					t.Errorf("%s: CRC32 %08X, want %08X", want.name, f.CRC32, want.crc)
				}
			}
		})
	}
}

func TestReaderLibarchiveContents(t *testing.T) {
	// The content of a stored file, checked against the text of the archive.
	b, err := os.ReadFile(filepath.Join("testdata", "libarchive", "test_read_format_rar.rar"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := readAllFile(readerOf(t, b).File[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := "test text document\r\n"; string(got) != want {
		t.Errorf("contents %q, want %q", got, want)
	}
}

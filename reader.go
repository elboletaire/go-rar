package rar

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Flags that make an archive or a file unsupported.
const (
	mainFlagVolume    = 0x0001
	mainFlagSolid     = 0x0008
	mainFlagEncHeader = 0x0080

	fileFlagSplitBefore = 0x0001
	fileFlagSplitAfter  = 0x0002
	fileFlagEncrypted   = 0x0004

	blockFlagAddSize = 0x8000 // ADD_SIZE follows the base header
)

// A Reader serves content from a RAR archive.
type Reader struct {
	r    io.ReaderAt
	File []*File

	// fileList is sorted by name for Open. It is built on first use, since
	// it synthesizes the directories the archive does not list.
	fileListOnce sync.Once
	fileList     []fileListEntry
}

// A ReadCloser is a [Reader] that must be closed when no longer needed.
type ReadCloser struct {
	f *os.File
	Reader
}

// A File is a single file in a RAR archive. The file information is in the
// embedded [FileHeader]. The contents are read with [File.Open].
type File struct {
	FileHeader
	r          io.ReaderAt
	dataOffset int64
}

// OpenReader opens the RAR file specified by name and returns a ReadCloser.
// A file name that is not local, or is a drive path, returns ErrInsecurePath
// along with the reader, as [NewReader] does.
func OpenReader(name string) (*ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	r := new(ReadCloser)
	if err = r.init(f, fi.Size()); err != nil && err != ErrInsecurePath {
		f.Close()
		return nil, err
	}
	r.f = f
	return r, err
}

// NewReader returns a new [Reader] reading from r, which is assumed to have
// the given size in bytes. A file name that is not [filepath.IsLocal], or is a
// drive path, returns the reader along with [ErrInsecurePath].
func NewReader(r io.ReaderAt, size int64) (*Reader, error) {
	if size < 0 {
		return nil, errors.New("rar: size cannot be negative")
	}
	rr := new(Reader)
	err := rr.init(r, size)
	if err != nil && err != ErrInsecurePath {
		return nil, err
	}
	return rr, err
}

// init reads every block of the archive, stopping at the end block or at the
// end of the data.
func (r *Reader) init(rdr io.ReaderAt, size int64) error {
	var marker [len(rarMarker)]byte
	if err := readFull(rdr, marker[:], 0, size); err != nil {
		return err
	}
	switch {
	case string(marker[:]) == rarMarker:
	case string(marker[:6]) == rarMarker[:6] && marker[6] == 0x01:
		return ErrUnsupported // RAR5
	default:
		return ErrFormat
	}
	r.r = rdr

	off := int64(len(rarMarker))
	for off < size {
		var base [7]byte
		if err := readFull(rdr, base[:], off, size); err != nil {
			return err
		}
		typ := base[2]
		flags := binary.LittleEndian.Uint16(base[3:])
		headSize := int64(binary.LittleEndian.Uint16(base[5:]))
		// Check the size before allocating, so a hostile HEAD_SIZE costs nothing.
		if headSize < 7 || headSize > size-off {
			return ErrFormat
		}
		head := make([]byte, headSize)
		if err := readFull(rdr, head, off, size); err != nil {
			return err
		}
		if binary.LittleEndian.Uint16(head) != uint16(crc32.ChecksumIEEE(head[2:])) {
			return ErrFormat
		}

		switch typ {
		case mainHeaderType:
			if flags&(mainFlagVolume|mainFlagSolid|mainFlagEncHeader) != 0 {
				return ErrUnsupported
			}
		case fileHeaderType:
			f, err := parseFileHeader(head, flags)
			if err != nil {
				return err
			}
			// The data follows the header and is PackedSize bytes long.
			dataOff := off + headSize
			if f.PackedSize > uint64(size-dataOff) {
				return ErrFormat
			}
			f.r = rdr
			f.dataOffset = dataOff
			r.File = append(r.File, f)
			off = dataOff + int64(f.PackedSize)
			continue
		case endBlockType:
			return r.checkNames()
		}

		// Other blocks are skipped, with their ADD_SIZE data if they have it.
		skip := uint64(headSize)
		if flags&blockFlagAddSize != 0 {
			if headSize < 11 {
				return ErrFormat
			}
			skip += uint64(binary.LittleEndian.Uint32(head[7:]))
		}
		if skip > uint64(size-off) {
			return ErrFormat
		}
		off += int64(skip)
	}
	return r.checkNames()
}

// checkNames returns ErrInsecurePath if a name could escape the directory it
// is extracted to.
func (r *Reader) checkNames() error {
	for _, f := range r.File {
		if f.Name == "" {
			// Like zip, an empty name is accepted.
			continue
		}
		if !filepath.IsLocal(f.Name) || hasDrive(f.Name) {
			return ErrInsecurePath
		}
	}
	return nil
}

// hasDrive reports whether name starts with a drive letter, such as "C:".
// filepath.IsLocal rejects these only on Windows, but archives come from any host.
func hasDrive(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	c := name[0] | 0x20
	return 'a' <= c && c <= 'z'
}

// readFull reads len(buf) bytes at off, which must lie within size. Data that
// ends early means the archive is truncated.
func readFull(r io.ReaderAt, buf []byte, off, size int64) error {
	if off < 0 || off > size || int64(len(buf)) > size-off {
		return ErrFormat
	}
	n, err := r.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil || err == io.EOF {
		return ErrFormat
	}
	return err
}

// parseFileHeader decodes a file header block. head is the whole block,
// including the base header, which the caller has already checked.
func parseFileHeader(head []byte, flags uint16) (*File, error) {
	b := head[7:]
	if len(b) < 25 {
		return nil, ErrFormat
	}
	packed := uint64(binary.LittleEndian.Uint32(b[0:]))
	unpacked := uint64(binary.LittleEndian.Uint32(b[4:]))
	nameAt := 25
	if flags&fileFlagLarge != 0 {
		if len(b) < 33 {
			return nil, ErrFormat
		}
		packed |= uint64(binary.LittleEndian.Uint32(b[25:])) << 32
		unpacked |= uint64(binary.LittleEndian.Uint32(b[29:])) << 32
		nameAt = 33
	}
	nameSize := int(binary.LittleEndian.Uint16(b[19:]))
	// Anything after the name, such as a salt or extended time, is ignored.
	if len(b)-nameAt < nameSize {
		return nil, ErrFormat
	}
	name := decodeName(b[nameAt:nameAt+nameSize], flags&fileFlagUnicode != 0)
	// RAR stores "\" on every host, as libarchive also assumes.
	name = strings.ReplaceAll(name, `\`, "/")
	if flags&fileFlagDir == fileFlagDir && !strings.HasSuffix(name, "/") {
		name += "/"
	}
	return &File{FileHeader: FileHeader{
		Name:         name,
		Modified:     dosToTime(binary.LittleEndian.Uint32(b[13:])),
		CRC32:        binary.LittleEndian.Uint32(b[9:]),
		PackedSize:   packed,
		UnpackedSize: unpacked,
		Method:       b[18],
		HostOS:       b[8],
		Attributes:   binary.LittleEndian.Uint32(b[21:]),
		Flags:        flags,
	}}, nil
}

// Close closes the RAR file, rendering it unusable for I/O.
func (rc *ReadCloser) Close() error {
	return rc.f.Close()
}

// DataOffset returns the offset of the file's data, relative to the start of
// the archive. The header was checked by NewReader, so the error is always nil.
func (f *File) DataOffset() (offset int64, err error) {
	return f.dataOffset, nil
}

// Open returns an io.ReadCloser for the File's contents. Reading to EOF checks
// the size and CRC32, and returns ErrChecksum on a mismatch. Split or encrypted
// files give ErrUnsupported, and methods other than Store give ErrAlgorithm.
func (f *File) Open() (io.ReadCloser, error) {
	rc, err := f.open()
	if err != nil {
		return nil, err
	}
	return rc, nil
}

// open is Open with the concrete type, which Reader.Open needs for Stat.
func (f *File) open() (*fileReader, error) {
	if f.Flags&(fileFlagSplitBefore|fileFlagSplitAfter|fileFlagEncrypted) != 0 {
		return nil, ErrUnsupported
	}
	if f.Method != Store {
		return nil, ErrAlgorithm
	}
	var data io.Reader = strings.NewReader("")
	if !f.isDir() {
		// A stored file has exactly its PackedSize bytes. Anything else would make
		// Stat report a size that callers such as fs.ReadFile allocate for.
		if f.UnpackedSize != f.PackedSize {
			return nil, ErrFormat
		}
		data = io.NewSectionReader(f.r, f.dataOffset, int64(f.PackedSize))
	}
	return &fileReader{f: f, r: data, hash: crc32.NewIEEE()}, nil
}

// fileReader reads the data of a File and checks it at EOF.
type fileReader struct {
	f     *File
	r     io.Reader
	hash  hash.Hash32
	nread uint64
	err   error // sticky error
}

func (r *fileReader) Stat() (fs.FileInfo, error) {
	return r.f.FileInfo(), nil
}

func (r *fileReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.r.Read(p)
	r.hash.Write(p[:n])
	r.nread += uint64(n)
	if r.nread > r.f.UnpackedSize {
		err = ErrChecksum
	} else if err == io.EOF && (r.nread != r.f.UnpackedSize || r.hash.Sum32() != r.f.CRC32) {
		err = ErrChecksum
	}
	r.err = err
	return n, err
}

func (r *fileReader) Close() error { return nil }

// fileListEntry is a file or a directory, for fs.FS.
type fileListEntry struct {
	name  string
	file  *File
	isDir bool
	isDup bool
}

type fileInfoDirEntry interface {
	fs.FileInfo
	fs.DirEntry
}

func (f *fileListEntry) stat() (fileInfoDirEntry, error) {
	if f.isDup {
		return nil, errors.New(f.name + ": duplicate entries in rar file")
	}
	if !f.isDir {
		return headerFileInfo{&f.file.FileHeader}, nil
	}
	return f, nil
}

// Only used for directories.
func (f *fileListEntry) Name() string      { _, elem, _ := split(f.name); return elem }
func (f *fileListEntry) Size() int64       { return 0 }
func (f *fileListEntry) Mode() fs.FileMode { return fs.ModeDir | 0555 }
func (f *fileListEntry) Type() fs.FileMode { return fs.ModeDir }
func (f *fileListEntry) IsDir() bool       { return true }
func (f *fileListEntry) Sys() any          { return nil }

func (f *fileListEntry) ModTime() time.Time {
	if f.file == nil {
		return time.Time{}
	}
	return f.file.Modified.UTC()
}

func (f *fileListEntry) Info() (fs.FileInfo, error) { return f, nil }

func (f *fileListEntry) String() string {
	return fs.FormatDirEntry(f)
}

// toValidName coerces name to be a valid name for fs.FS.Open.
func toValidName(name string) string {
	name = strings.ReplaceAll(name, `\`, `/`)
	p := path.Clean(name)

	p = strings.TrimPrefix(p, "/")

	for strings.HasPrefix(p, "../") {
		p = p[len("../"):]
	}

	return p
}

func (r *Reader) initFileList() {
	r.fileListOnce.Do(func() {
		// Preallocate the minimum size of the index.
		// We may also synthesize additional directory entries.
		r.fileList = make([]fileListEntry, 0, len(r.File))
		// files and knownDirs map from a file/directory name
		// to an index into the r.fileList entry that we are
		// building. They are used to mark duplicate entries.
		files := make(map[string]int)
		knownDirs := make(map[string]int)

		// dirs[name] is true if name is known to be a directory,
		// because it appears as a prefix in a path.
		dirs := make(map[string]bool)

		for _, file := range r.File {
			isDir := len(file.Name) > 0 && file.Name[len(file.Name)-1] == '/'
			name := toValidName(file.Name)
			if name == "" {
				continue
			}

			if idx, ok := files[name]; ok {
				r.fileList[idx].isDup = true
				continue
			}
			if idx, ok := knownDirs[name]; ok {
				r.fileList[idx].isDup = true
				continue
			}

			dir := name
			for {
				if idx := strings.LastIndex(dir, "/"); idx < 0 {
					break
				} else {
					dir = dir[:idx]
				}
				if dirs[dir] {
					break
				}
				dirs[dir] = true
			}

			idx := len(r.fileList)
			entry := fileListEntry{
				name:  name,
				file:  file,
				isDir: isDir,
			}
			r.fileList = append(r.fileList, entry)
			if isDir {
				knownDirs[name] = idx
			} else {
				files[name] = idx
			}
		}
		for dir := range dirs {
			if _, ok := knownDirs[dir]; !ok {
				if idx, ok := files[dir]; ok {
					r.fileList[idx].isDup = true
				} else {
					entry := fileListEntry{
						name:  dir,
						file:  nil,
						isDir: true,
					}
					r.fileList = append(r.fileList, entry)
				}
			}
		}

		slices.SortFunc(r.fileList, func(a, b fileListEntry) int {
			return fileEntryCompare(a.name, b.name)
		})
	})
}

func fileEntryCompare(x, y string) int {
	xdir, xelem, _ := split(x)
	ydir, yelem, _ := split(y)
	if xdir != ydir {
		return strings.Compare(xdir, ydir)
	}
	return strings.Compare(xelem, yelem)
}

// Open opens the named file in the RAR archive, using the semantics of
// fs.FS.Open: paths are always slash separated, with no leading / or ../
// elements. Directories that the archive does not list are synthesized.
func (r *Reader) Open(name string) (fs.File, error) {
	r.initFileList()

	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	e := r.openLookup(name)
	if e == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if e.isDir {
		return &openDir{e, r.openReadDir(name), 0}, nil
	}
	rc, err := e.file.open()
	if err != nil {
		return nil, err
	}
	return rc, nil
}

func split(name string) (dir, elem string, isDir bool) {
	name, isDir = strings.CutSuffix(name, "/")
	i := strings.LastIndexByte(name, '/')
	if i < 0 {
		return ".", name, isDir
	}
	return name[:i], name[i+1:], isDir
}

var dotFile = &fileListEntry{name: "./", isDir: true}

func (r *Reader) openLookup(name string) *fileListEntry {
	if name == "." {
		return dotFile
	}

	dir, elem, _ := split(name)
	files := r.fileList
	i, _ := slices.BinarySearchFunc(files, dir, func(a fileListEntry, dir string) (ret int) {
		idir, ielem, _ := split(a.name)
		if dir != idir {
			return strings.Compare(idir, dir)
		}
		return strings.Compare(ielem, elem)
	})
	if i < len(files) {
		fname := files[i].name
		if fname == name || len(fname) == len(name)+1 && fname[len(name)] == '/' && fname[:len(name)] == name {
			return &files[i]
		}
	}
	return nil
}

func (r *Reader) openReadDir(dir string) []fileListEntry {
	files := r.fileList
	i, _ := slices.BinarySearchFunc(files, dir, func(a fileListEntry, dir string) int {
		idir, _, _ := split(a.name)
		if dir != idir {
			return strings.Compare(idir, dir)
		}
		// find the first entry with dir
		return +1
	})
	j, _ := slices.BinarySearchFunc(files, dir, func(a fileListEntry, dir string) int {
		jdir, _, _ := split(a.name)
		if dir != jdir {
			return strings.Compare(jdir, dir)
		}
		// find the last entry with dir
		return -1
	})
	return files[i:j]
}

type openDir struct {
	e      *fileListEntry
	files  []fileListEntry
	offset int
}

func (d *openDir) Close() error               { return nil }
func (d *openDir) Stat() (fs.FileInfo, error) { return d.e.stat() }

func (d *openDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.e.name, Err: errors.New("is a directory")}
}

func (d *openDir) ReadDir(count int) ([]fs.DirEntry, error) {
	n := len(d.files) - d.offset
	if count > 0 && n > count {
		n = count
	}
	if n == 0 {
		if count <= 0 {
			return nil, nil
		}
		return nil, io.EOF
	}
	list := make([]fs.DirEntry, n)
	for i := range list {
		s, err := d.files[d.offset+i].stat()
		if err != nil {
			return nil, err
		} else if s.Name() == "." || !fs.ValidPath(s.Name()) {
			return nil, &fs.PathError{
				Op:   "readdir",
				Path: d.e.name,
				Err:  fmt.Errorf("invalid file name: %v", d.files[d.offset+i].name),
			}
		}
		list[i] = s
	}
	d.offset += n
	return list, nil
}

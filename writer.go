package rar

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Errors returned by Writer.
var (
	errWriterClosed = errors.New("rar: writer is closed")
	errEntryClosed  = errors.New("rar: write to closed entry")
	errDirWrite     = errors.New("rar: write to directory entry")
)

// maxFileNameSize is the longest FILE_NAME that fits. NAME_SIZE and HEAD_SIZE
// are 16 bits, and HEAD_SIZE also counts the 40 fixed bytes of a LARGE header.
const maxFileNameSize = math.MaxUint16 - 40

// unpackVersion is UNP_VER, the RAR 2.9 decoder version.
const unpackVersion = 29

// A Writer writes a RAR archive to an underlying io.Writer. Each entry is
// buffered in memory until the next entry starts or the archive is closed, so
// its header can carry the final size and CRC32.
type Writer struct {
	w       io.Writer
	cur     *fileWriter // entry whose header and data are not written yet
	started bool        // marker and main header are written
	closed  bool
	err     error // first error from the underlying writer, returned from then on
}

// NewWriter returns a Writer that writes a RAR archive to w. It writes nothing
// until the first entry is added or Close is called.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Create adds a file with the given name to the archive, modified now. It is
// shorthand for CreateHeader with only Name and Modified set.
func (w *Writer) Create(name string) (io.Writer, error) {
	return w.CreateHeader(&FileHeader{
		Name:     name,
		Modified: time.Now(),
	})
}

// CreateHeader adds an entry described by fh and returns a writer for its data,
// which is buffered until the next entry or Close. Size, CRC32 and Flags of fh are
// ignored and computed; a trailing "/" or fs.ModeDir makes a directory.
func (w *Writer) CreateHeader(fh *FileHeader) (io.Writer, error) {
	if w.err != nil {
		return nil, w.err
	}
	if w.closed {
		return nil, errWriterClosed
	}
	if fh.Method != 0 && fh.Method != Store {
		return nil, ErrAlgorithm
	}

	// Zero Attributes means unset, so the defaults apply whatever the host.
	mode := fs.FileMode(0666)
	if fh.Attributes == 0 {
		if strings.HasSuffix(fh.Name, "/") {
			mode = fs.ModeDir | 0777
		}
	} else {
		mode = fh.Mode()
	}
	dir := mode.IsDir()
	name, err := entryName(fh.Name, dir)
	if err != nil {
		return nil, err
	}
	enc, unicode := encodeName(strings.ReplaceAll(strings.TrimSuffix(name, "/"), "/", `\`))
	if len(enc) > maxFileNameSize {
		return nil, fmt.Errorf("rar: file name %q is too long", name)
	}

	if err := w.flush(); err != nil {
		return nil, err
	}

	h := *fh
	h.Name = name
	if fh.Attributes == 0 || isUnixHost(fh.HostOS) {
		h.SetMode(mode)
	} else {
		// Win32 attributes are kept as given. Only the directory bit follows the name.
		h.HostOS = HostWin32
		h.Attributes &^= attrDir
		if dir {
			h.Attributes |= attrDir
		}
	}
	h.Method = Store
	h.CRC32, h.PackedSize, h.UnpackedSize = 0, 0, 0
	h.Flags = 0
	if unicode {
		h.Flags |= fileFlagUnicode
	}
	if dir {
		h.Flags |= fileFlagDir
	}
	w.cur = &fileWriter{zw: w, fh: h, name: enc, dir: dir}
	return w.cur, nil
}

// AddFS adds the directories and regular files of fsys to the archive, in
// lexical order. The root directory is not added. Other file types are an
// error.
func (w *Writer) AddFS(fsys fs.FS) error {
	return fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("rar: %s: unsupported file type %v", name, info.Mode().Type())
		}
		fh, err := FileInfoHeader(info)
		if err != nil {
			return err
		}
		// FileInfoHeader only knows the base name.
		fh.Name = name
		if d.IsDir() {
			fh.Name += "/"
		}
		fw, err := w.CreateHeader(fh)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		return copyFile(fw, fsys, name)
	})
}

// Close writes the pending entry and the end block. It does not close the
// underlying writer. Calling Close again returns an error.
func (w *Writer) Close() error {
	if w.closed {
		return errWriterClosed
	}
	w.closed = true
	if err := w.flush(); err != nil {
		return err
	}
	if err := w.start(); err != nil {
		return err
	}
	return w.write(finishBlock(baseHeader(endBlockType, endBlockFlags, 7)))
}

// write writes p to the underlying writer. The first failure is kept, so later
// calls report it and a broken archive is never completed.
func (w *Writer) write(p []byte) error {
	if w.err != nil {
		return w.err
	}
	_, err := w.w.Write(p)
	w.err = err
	return err
}

// start writes the marker and main header, if they are not written yet.
func (w *Writer) start() error {
	if w.started {
		return nil
	}
	w.started = true
	main := baseHeader(mainHeaderType, 0, 13)
	main = append(main, make([]byte, 6)...) // RESERVED1 and RESERVED2
	return w.write(append([]byte(rarMarker), finishBlock(main)...))
}

// flush writes the pending entry, its header then its data.
func (w *Writer) flush() error {
	f := w.cur
	if f == nil {
		return nil
	}
	w.cur = nil
	if err := w.start(); err != nil {
		return err
	}
	f.fh.PackedSize = uint64(len(f.data))
	f.fh.UnpackedSize = f.fh.PackedSize
	f.fh.CRC32 = crc32.ChecksumIEEE(f.data)
	if err := w.write(fileHeaderBytes(&f.fh, f.name)); err != nil {
		return err
	}
	return w.write(f.data)
}

// fileWriter buffers the data of one entry until it is flushed.
type fileWriter struct {
	zw   *Writer
	fh   FileHeader
	name []byte // FILE_NAME as stored
	dir  bool
	data []byte
}

// Write buffers p as file data. It fails once the entry is no longer the
// current one, and for directories it accepts only empty writes.
func (f *fileWriter) Write(p []byte) (int, error) {
	if f.zw.cur != f {
		return 0, errEntryClosed
	}
	if f.zw.err != nil {
		return 0, f.zw.err
	}
	if f.dir && len(p) > 0 {
		return 0, errDirWrite
	}
	f.data = append(f.data, p...)
	return len(p), nil
}

// entryName returns the API name of an entry: no leading "/", and a trailing
// "/" for directories. A backslash is rejected, as it is the on-disk separator.
func entryName(name string, dir bool) (string, error) {
	name = strings.TrimLeft(name, "/")
	if dir {
		name = strings.TrimRight(name, "/")
	}
	if name == "" || strings.ContainsAny(name, "\x00\\") || !utf8.ValidString(name) {
		return "", fmt.Errorf("rar: invalid file name %q", name)
	}
	if dir {
		name += "/"
	}
	return name, nil
}

// isUnixHost reports whether the Attributes of hostOS hold a Unix st_mode.
func isUnixHost(hostOS uint8) bool {
	return hostOS == HostUnix || hostOS == HostMacOS || hostOS == HostBeOS
}

// copyFile writes the contents of name in fsys to dst.
func copyFile(dst io.Writer, fsys fs.FS, name string) error {
	f, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(dst, f)
	return err
}

// baseHeader returns the 7-byte block header with HEAD_CRC still zero.
func baseHeader(typ byte, flags, size uint16) []byte {
	b := make([]byte, 7)
	b[2] = typ
	binary.LittleEndian.PutUint16(b[3:], flags)
	binary.LittleEndian.PutUint16(b[5:], size)
	return b
}

// finishBlock sets HEAD_CRC to the low 16 bits of the CRC32 over the rest of
// the block.
func finishBlock(b []byte) []byte {
	binary.LittleEndian.PutUint16(b, uint16(crc32.ChecksumIEEE(b[2:])))
	return b
}

// fileHeaderBytes returns the file header block for fh, followed by name as the
// encoded FILE_NAME. It sets ADD_SIZE, and LARGE when a size needs 64 bits.
func fileHeaderBytes(fh *FileHeader, name []byte) []byte {
	flags := fh.Flags | fileFlagAddSize
	size := 32 + len(name)
	large := fh.PackedSize > math.MaxUint32 || fh.UnpackedSize > math.MaxUint32
	if large {
		flags |= fileFlagLarge
		size += 8
	}

	b := baseHeader(fileHeaderType, flags, uint16(size))
	b = binary.LittleEndian.AppendUint32(b, uint32(fh.PackedSize))
	b = binary.LittleEndian.AppendUint32(b, uint32(fh.UnpackedSize))
	b = append(b, fh.HostOS)
	b = binary.LittleEndian.AppendUint32(b, fh.CRC32)
	b = binary.LittleEndian.AppendUint32(b, timeToDOS(fh.Modified))
	b = append(b, unpackVersion, Store)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(name)))
	b = binary.LittleEndian.AppendUint32(b, fh.Attributes)
	if large {
		b = binary.LittleEndian.AppendUint32(b, uint32(fh.PackedSize>>32))
		b = binary.LittleEndian.AppendUint32(b, uint32(fh.UnpackedSize>>32))
	}
	b = append(b, name...)
	return finishBlock(b)
}

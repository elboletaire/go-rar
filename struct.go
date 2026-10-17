// Package rar writes RAR 2.9-4.x archives using only the Store method. Its API
// follows archive/zip, so code that uses zip.NewWriter can switch to NewWriter
// with few changes.
package rar

import (
	"errors"
	"io/fs"
	"path"
	"time"
)

// Store is the RAR method for uncompressed data.
const Store uint8 = 0x30

// Host operating systems, as stored in FileHeader.HostOS.
const (
	HostMSDOS uint8 = 0
	HostWin32 uint8 = 2
	HostUnix  uint8 = 3
	HostMacOS uint8 = 4
	HostBeOS  uint8 = 5
)

// Errors returned by this package.
var (
	ErrFormat       = errors.New("rar: not a valid rar file")
	ErrAlgorithm    = errors.New("rar: unsupported compression algorithm")
	ErrChecksum     = errors.New("rar: checksum error")
	ErrUnsupported  = errors.New("rar: unsupported archive feature")
	ErrInsecurePath = errors.New("rar: insecure file path")
)

// Block types and flags from the RAR technote.
const (
	rarMarker = "Rar!\x1a\x07\x00"

	mainHeaderType = 0x73
	fileHeaderType = 0x74
	endBlockType   = 0x7B

	endBlockFlags   = 0x4000
	fileFlagAddSize = 0x8000 // PACK_SIZE acts as ADD_SIZE; always set on file headers
	fileFlagLarge   = 0x0100 // HIGH_PACK_SIZE and HIGH_UNP_SIZE are present
	fileFlagUnicode = 0x0200 // FILE_NAME has an encoded UTF-16 part
	fileFlagDir     = 0x00E0 // dictionary bits set mark a directory
)

// Win32 attributes, as stored in FileHeader.Attributes.
const (
	attrReadOnly = 0x01
	attrDir      = 0x10
	attrArchive  = 0x20
)

// Unix st_mode bits, as stored in FileHeader.Attributes when HostOS is HostUnix.
const (
	unixTypeMask    = 0xF000
	unixTypeFIFO    = 0x1000
	unixTypeChar    = 0x2000
	unixTypeDir     = 0x4000
	unixTypeBlock   = 0x6000
	unixTypeSymlink = 0xA000
	unixTypeSocket  = 0xC000
	unixSetuid      = 0x0800
	unixSetgid      = 0x0400
	unixSticky      = 0x0200
)

// FileHeader describes a file within a RAR archive.
type FileHeader struct {
	// Name is the name of the file, using "/" as the separator. A trailing
	// "/" marks a directory.
	Name string

	// Modified is the modification time. It is stored as an MS-DOS time with
	// 2s resolution, clamped to the years 1980 through 2107.
	Modified time.Time

	// CRC32 is the IEEE CRC32 of the file data, zero for directories.
	CRC32 uint32

	// PackedSize is the size of the data stored in the archive.
	PackedSize uint64

	// UnpackedSize is the size of the file data.
	UnpackedSize uint64

	// Method is the compression method. Zero means Store.
	Method uint8

	// HostOS is the operating system that wrote the file, one of the Host
	// constants.
	HostOS uint8

	// Attributes holds the host-specific attributes: Win32 attributes, or
	// the Unix st_mode for HostUnix.
	Attributes uint32

	// Flags are the RAR file header flags, such as the large and Unicode
	// bits.
	Flags uint16
}

// FileInfoHeader creates a partially populated FileHeader from an fs.FileInfo.
// The name is the base name, so set Name to the full path when it matters.
// Directories get a trailing "/".
func FileInfoHeader(fi fs.FileInfo) (*FileHeader, error) {
	fh := &FileHeader{
		Name:         fi.Name(),
		Modified:     fi.ModTime(),
		UnpackedSize: uint64(fi.Size()),
	}
	if fi.IsDir() {
		fh.Name += "/"
	}
	fh.SetMode(fi.Mode())
	return fh, nil
}

// FileInfo returns an fs.FileInfo for the FileHeader.
func (h *FileHeader) FileInfo() fs.FileInfo {
	return headerFileInfo{h}
}

// headerFileInfo implements fs.FileInfo.
type headerFileInfo struct {
	fh *FileHeader
}

func (fi headerFileInfo) Name() string       { return path.Base(fi.fh.Name) }
func (fi headerFileInfo) Size() int64        { return int64(fi.fh.UnpackedSize) }
func (fi headerFileInfo) IsDir() bool        { return fi.Mode().IsDir() }
func (fi headerFileInfo) ModTime() time.Time { return fi.fh.Modified }
func (fi headerFileInfo) Mode() fs.FileMode  { return fi.fh.Mode() }
func (fi headerFileInfo) Sys() any           { return fi.fh }

// Mode returns the permission and type bits of the file. Win32 and DOS
// attributes map to 0666 for files and 0777 for directories, with the write
// bits cleared for read-only files. Unix, Mac OS and BeOS attributes are st_mode.
func (h *FileHeader) Mode() fs.FileMode {
	if h.HostOS == HostUnix || h.HostOS == HostMacOS || h.HostOS == HostBeOS {
		mode := unixModeToFileMode(h.Attributes)
		if h.isDir() {
			mode |= fs.ModeDir
		}
		return mode
	}

	mode := fs.FileMode(0666)
	if h.isDir() || h.Attributes&attrDir != 0 {
		mode = fs.ModeDir | 0777
	}
	if h.Attributes&attrReadOnly != 0 {
		mode &^= 0222
	}
	return mode
}

// SetMode sets HostOS to HostWin32 and the Win32 attributes from mode. A
// mode with no write bits gets the read-only attribute.
func (h *FileHeader) SetMode(mode fs.FileMode) {
	h.HostOS = HostWin32
	h.Attributes = attrArchive
	if mode.IsDir() {
		h.Attributes = attrDir
	}
	if mode&0222 == 0 {
		h.Attributes |= attrReadOnly
	}
}

// isDir reports whether the name marks a directory.
func (h *FileHeader) isDir() bool {
	return len(h.Name) > 0 && h.Name[len(h.Name)-1] == '/'
}

// unixModeToFileMode converts a Unix st_mode into an fs.FileMode.
func unixModeToFileMode(m uint32) fs.FileMode {
	mode := fs.FileMode(m & 0777)
	switch m & unixTypeMask {
	case unixTypeBlock:
		mode |= fs.ModeDevice
	case unixTypeChar:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case unixTypeDir:
		mode |= fs.ModeDir
	case unixTypeFIFO:
		mode |= fs.ModeNamedPipe
	case unixTypeSymlink:
		mode |= fs.ModeSymlink
	case unixTypeSocket:
		mode |= fs.ModeSocket
	}
	if m&unixSetgid != 0 {
		mode |= fs.ModeSetgid
	}
	if m&unixSetuid != 0 {
		mode |= fs.ModeSetuid
	}
	if m&unixSticky != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

package rar

import (
	"bytes"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"
)

func TestRarMarker(t *testing.T) {
	want := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}
	if !bytes.Equal([]byte(rarMarker), want) {
		t.Fatalf("rarMarker = % X, want % X", []byte(rarMarker), want)
	}
}

func TestMode(t *testing.T) {
	tests := []struct {
		name   string
		header FileHeader
		want   fs.FileMode
	}{
		{"win32 file", FileHeader{Name: "a.txt", HostOS: HostWin32, Attributes: attrArchive}, 0666},
		{"win32 read-only file", FileHeader{Name: "a.txt", HostOS: HostWin32, Attributes: attrArchive | attrReadOnly}, 0444},
		{"win32 dir", FileHeader{Name: "d", HostOS: HostWin32, Attributes: attrDir}, fs.ModeDir | 0777},
		{"win32 read-only dir", FileHeader{Name: "d", HostOS: HostWin32, Attributes: attrDir | attrReadOnly}, fs.ModeDir | 0555},
		{"dir from trailing slash", FileHeader{Name: "d/", HostOS: HostWin32}, fs.ModeDir | 0777},
		{"msdos file", FileHeader{Name: "a.txt", HostOS: HostMSDOS, Attributes: attrArchive}, 0666},
		{"unix file", FileHeader{Name: "a.txt", HostOS: HostUnix, Attributes: 0o100644}, 0644},
		{"unix dir", FileHeader{Name: "d/", HostOS: HostUnix, Attributes: 0o40755}, fs.ModeDir | 0755},
		{"unix symlink", FileHeader{Name: "l", HostOS: HostUnix, Attributes: 0o120777}, fs.ModeSymlink | 0777},
		{"unix setuid", FileHeader{Name: "a", HostOS: HostUnix, Attributes: 0o104755}, fs.ModeSetuid | 0755},
		{"mac os file", FileHeader{Name: "a.txt", HostOS: HostMacOS, Attributes: 0o100644}, 0644},
		{"mac os dir", FileHeader{Name: "d/", HostOS: HostMacOS, Attributes: 0o40755}, fs.ModeDir | 0755},
		{"beos file", FileHeader{Name: "a.txt", HostOS: HostBeOS, Attributes: 0o100600}, 0600},
		{"beos dir", FileHeader{Name: "d/", HostOS: HostBeOS, Attributes: 0o40700}, fs.ModeDir | 0700},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.header.Mode(); got != tt.want {
				t.Errorf("Mode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetMode(t *testing.T) {
	// Win32 keeps only the write bit distinction, so 0644 comes back as 0666.
	tests := []struct {
		name     string
		mode     fs.FileMode
		wantAttr uint32
		wantMode fs.FileMode
	}{
		{"file", 0644, attrArchive, 0666},
		{"read-only file", 0444, attrArchive | attrReadOnly, 0444},
		{"dir", fs.ModeDir | 0755, attrDir, fs.ModeDir | 0777},
		{"read-only dir", fs.ModeDir | 0555, attrDir | attrReadOnly, fs.ModeDir | 0555},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h FileHeader
			h.SetMode(tt.mode)
			if h.HostOS != HostWin32 {
				t.Errorf("HostOS = %d, want %d", h.HostOS, HostWin32)
			}
			if h.Attributes != tt.wantAttr {
				t.Errorf("Attributes = %#x, want %#x", h.Attributes, tt.wantAttr)
			}
			if got := h.Mode(); got != tt.wantMode {
				t.Errorf("Mode() = %v, want %v", got, tt.wantMode)
			}
		})
	}
}

func TestFileInfoHeader(t *testing.T) {
	modTime := time.Date(2024, 10, 17, 13, 45, 58, 0, time.UTC)
	fsys := fstest.MapFS{
		"dir/file.txt": {Data: []byte("hello"), Mode: 0644, ModTime: modTime},
		"dir/ro.txt":   {Data: []byte("hello"), Mode: 0444, ModTime: modTime},
	}
	tests := []struct {
		path     string
		wantName string
		wantSize uint64
		wantAttr uint32
	}{
		{"dir", "dir/", 0, attrDir | attrReadOnly}, // synthesized MapFS directory is 0555
		{"dir/file.txt", "file.txt", 5, attrArchive},
		{"dir/ro.txt", "ro.txt", 5, attrArchive | attrReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			fi, err := fs.Stat(fsys, tt.path)
			if err != nil {
				t.Fatal(err)
			}
			h, err := FileInfoHeader(fi)
			if err != nil {
				t.Fatal(err)
			}
			if h.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", h.Name, tt.wantName)
			}
			if h.UnpackedSize != tt.wantSize {
				t.Errorf("UnpackedSize = %d, want %d", h.UnpackedSize, tt.wantSize)
			}
			if !h.Modified.Equal(fi.ModTime()) {
				t.Errorf("Modified = %v, want %v", h.Modified, fi.ModTime())
			}
			if h.HostOS != HostWin32 || h.Attributes != tt.wantAttr {
				t.Errorf("HostOS, Attributes = %d, %#x; want %d, %#x",
					h.HostOS, h.Attributes, HostWin32, tt.wantAttr)
			}
		})
	}
}

func TestFileInfo(t *testing.T) {
	modTime := time.Date(2024, 10, 17, 13, 45, 58, 0, time.UTC)
	tests := []struct {
		name     string
		header   FileHeader
		wantName string
		wantSize int64
		wantDir  bool
	}{
		{"file", FileHeader{Name: "a/b/file.txt", UnpackedSize: 42, HostOS: HostWin32, Attributes: attrArchive}, "file.txt", 42, false},
		{"dir", FileHeader{Name: "a/b/", HostOS: HostWin32, Attributes: attrDir}, "b", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.header
			h.Modified = modTime
			fi := h.FileInfo()
			if fi.Name() != tt.wantName {
				t.Errorf("Name() = %q, want %q", fi.Name(), tt.wantName)
			}
			if fi.Size() != tt.wantSize {
				t.Errorf("Size() = %d, want %d", fi.Size(), tt.wantSize)
			}
			if fi.IsDir() != tt.wantDir {
				t.Errorf("IsDir() = %v, want %v", fi.IsDir(), tt.wantDir)
			}
			if !fi.ModTime().Equal(modTime) {
				t.Errorf("ModTime() = %v, want %v", fi.ModTime(), modTime)
			}
			if fi.Mode() != h.Mode() {
				t.Errorf("Mode() = %v, want %v", fi.Mode(), h.Mode())
			}
			if fi.Sys() != &h {
				t.Errorf("Sys() does not return the header")
			}
		})
	}
}

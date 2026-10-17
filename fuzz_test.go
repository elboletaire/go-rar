package rar

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strings"
	"testing"
	"time"
)

// allocBytes returns the cumulative bytes allocated by the program. It is
// read without stopping the world, unlike runtime.ReadMemStats. The count is
// approximate: small allocations appear only when per-thread caches flush, so
// the limit in FuzzReader is a rough bound, not a precise one.
func allocBytes() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

func FuzzReader(f *testing.F) {
	f.Add([]byte{})
	f.Add(writeSeedArchive(f))
	seeds, err := filepath.Glob(filepath.Join("testdata", "libarchive", "*.rar"))
	if err != nil {
		f.Fatal(err)
	}
	for _, name := range seeds {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Each header and each file is charged to the input, so allocations should
		// stay within a constant factor of its size.
		limit := 64<<10 + 64*uint64(len(data))
		used := measureAllocs(data)
		// The counter is process-wide, so the fuzz engine's own allocations can
		// show up. A real overrun repeats, so only the minimum of a few runs counts.
		for i := 0; i < 2 && used > limit; i++ {
			used = min(used, measureAllocs(data))
		}
		if used > limit {
			t.Fatalf("allocated %d bytes for a %d byte input", used, len(data))
		}
	})
}

// readAllFiles walks the archive in data as an fs.FS, reads every regular file
// and opens every file through File.Open. It must not panic, whatever data is.
func readAllFiles(data []byte) {
	rr, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && err != ErrInsecurePath {
		return
	}
	for _, file := range rr.File {
		rc, err := file.Open()
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, rc)
		rc.Close()
	}
	_ = fs.WalkDir(rr, ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			_, _ = fs.ReadFile(rr, name)
		}
		return nil
	})
}

// measureAllocs returns the bytes allocated by readAllFiles(data).
func measureAllocs(data []byte) uint64 {
	before := allocBytes()
	readAllFiles(data)
	return allocBytes() - before
}

// writeSeedArchive returns a small archive with directories, implied directories
// and a Unicode name, for seeding FuzzReader.
func writeSeedArchive(f *testing.F) []byte {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, name := range []string{"d/", "d/ページ.txt", "e/f/g.bin", "empty"} {
		fw, err := w.CreateHeader(&FileHeader{Name: name, Modified: time.Date(2024, 1, 2, 3, 4, 6, 0, time.UTC)})
		if err != nil {
			f.Fatal(err)
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		if _, err := fw.Write([]byte("seed " + name)); err != nil {
			f.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	return buf.Bytes()
}

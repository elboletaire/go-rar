package rar

import (
	"bytes"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// oracleTime is the modification time of every oracle entry. DOS time keeps
// 2s resolution, so extracted files must match it truncated to 2s.
var oracleTime = time.Date(2024, 3, 5, 12, 34, 57, 0, time.UTC)

// oracleFile is an entry of the oracle archive.
type oracleFile struct {
	name string
	dir  bool
	data []byte
}

// oracleFiles returns the entries of the oracle archive. The directories "a" and
// "a/b" are not added explicitly, so the tools must cope with implied ones.
func oracleFiles() []oracleFile {
	// Seeded, so the test is reproducible.
	big := make([]byte, 300<<10)
	rng := rand.NewChaCha8([32]byte{'o', 'r', 'a', 'c', 'l', 'e'})
	_, _ = rng.Read(big)

	return []oracleFile{
		{name: "Ñandú/", dir: true},
		{name: "Ñandú/ページ 01.jpg", data: big},
		{name: "😀.png", data: []byte("\x89PNG small")},
		{name: "empty.txt"},
		{name: "a/b/c.txt", data: []byte("nested\n")},
	}
}

// writeOracleArchive writes the oracle archive to a temporary file and returns
// its path.
func writeOracleArchive(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oracle.rar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(f)
	for _, of := range oracleFiles() {
		fw, err := w.CreateHeader(&FileHeader{Name: of.name, Modified: oracleTime})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(of.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// oracleTool returns the path of the named tool, or skips the test when it is
// not on PATH.
func oracleTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		// CI sets RAR_ORACLES=require so a missing tool fails instead of silently skipping.
		if os.Getenv("RAR_ORACLES") == "require" {
			t.Fatalf("%s not found: %v", name, err)
		}
		t.Skipf("%s not found: %v", name, err)
	}
	return path
}

// runOracle runs tool and returns its combined output. TZ and the locale are
// fixed, so DOS times and UTF-8 names come out the same everywhere.
func runOracle(t *testing.T, tool string, args ...string) string {
	t.Helper()
	cmd := exec.Command(tool, args...)
	cmd.Env = append(os.Environ(), "TZ=UTC", "LANG=C.UTF-8", "LC_ALL=C.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", tool, args, err, out)
	}
	return string(out)
}

// sltSizes maps each path in `7z l -slt` output to its size. The entries follow
// the "----------" line; the archive header above it is skipped.
func sltSizes(t *testing.T, out string) map[string]int64 {
	t.Helper()
	sizes := make(map[string]int64)
	var path string
	inEntries := false
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "----------"):
			inEntries = true
		case !inEntries:
		case strings.HasPrefix(line, "Path = "):
			path = strings.TrimPrefix(line, "Path = ")
		case strings.HasPrefix(line, "Size = "):
			n, err := strconv.ParseInt(strings.TrimPrefix(line, "Size = "), 10, 64)
			if err != nil {
				t.Fatalf("bad size line %q: %v", line, err)
			}
			sizes[path] = n
		}
	}
	return sizes
}

// checkExtracted compares the tree under dir with the oracle archive: the same
// names and no others, identical data, and the modification time to 2s.
func checkExtracted(t *testing.T, dir string) {
	t.Helper()
	want := map[string]oracleFile{
		"a":   {name: "a", dir: true},
		"a/b": {name: "a/b", dir: true},
	}
	for _, of := range oracleFiles() {
		want[strings.TrimSuffix(of.name, "/")] = of
	}

	got := make(map[string]bool)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		got[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected extracted path %q", name)
		}
	}

	for name, of := range want {
		p := filepath.Join(dir, filepath.FromSlash(name))
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("%q not extracted: %v", name, err)
			continue
		}
		if of.dir {
			if !info.IsDir() {
				t.Errorf("%q is not a directory", name)
			}
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if !bytes.Equal(data, of.data) {
			t.Errorf("%q: extracted data differs (%d bytes, want %d)", name, len(data), len(of.data))
		}
		if mtime := oracleTime.Truncate(2 * time.Second); !info.ModTime().Equal(mtime) {
			t.Errorf("%q: mtime %v, want %v", name, info.ModTime(), mtime)
		}
	}
}

func TestOracle7zTest(t *testing.T) {
	tool := oracleTool(t, "7z")
	out := runOracle(t, tool, "t", writeOracleArchive(t))
	if !strings.Contains(out, "Everything is Ok") {
		t.Errorf("7z t did not report Everything is Ok:\n%s", out)
	}
}

func TestOracle7zList(t *testing.T) {
	tool := oracleTool(t, "7z")
	sizes := sltSizes(t, runOracle(t, tool, "l", "-slt", writeOracleArchive(t)))
	for _, of := range oracleFiles() {
		name := strings.TrimSuffix(of.name, "/")
		size, ok := sizes[name]
		if !ok {
			t.Errorf("7z l does not list %q", name)
			continue
		}
		if want := int64(len(of.data)); size != want {
			t.Errorf("7z l: %q size %d, want %d", name, size, want)
		}
	}
}

func TestOracle7zExtract(t *testing.T) {
	tool := oracleTool(t, "7z")
	dir := t.TempDir()
	runOracle(t, tool, "x", "-y", "-o"+dir, writeOracleArchive(t))
	checkExtracted(t, dir)
}

func TestOracleBsdtarExtract(t *testing.T) {
	tool := oracleTool(t, "bsdtar")
	dir := t.TempDir()
	runOracle(t, tool, "-xf", writeOracleArchive(t), "-C", dir)
	checkExtracted(t, dir)
}

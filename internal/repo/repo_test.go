package repo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTarGz builds an in-memory tar.gz from a name→content map. All
// entries are regular files; intermediate dirs are auto-created on
// extraction.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	// Sort names so test output is deterministic.
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	// stable order
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	for _, name := range names {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
			Mode:     0o644,
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	r, err := New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestStage_WritesPackageAndSHA256(t *testing.T) {
	r := newTestRepo(t)
	body := makeTarGz(t, map[string]string{
		"manifest.yaml": "schema_version: 1\n",
	})
	res, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(body), 10*1024*1024)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if res.Size != int64(len(body)) {
		t.Errorf("Size=%d, want %d", res.Size, len(body))
	}
	if res.SHA256Hex == "" {
		t.Errorf("SHA256Hex empty")
	}
	// package.tar.gz and package.sha256 must exist in staging.
	if _, err := os.Stat(filepath.Join(res.StagingDir, "package.tar.gz")); err != nil {
		t.Errorf("stat package.tar.gz: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.StagingDir, "package.sha256")); err != nil {
		t.Errorf("stat package.sha256: %v", err)
	}
	shaSidecar, _ := os.ReadFile(filepath.Join(res.StagingDir, "package.sha256"))
	if strings.TrimSpace(string(shaSidecar)) != res.SHA256Hex {
		t.Errorf("sidecar sha256 = %q, want %q", shaSidecar, res.SHA256Hex)
	}
}

func TestStage_RejectsOversize(t *testing.T) {
	r := newTestRepo(t)
	// 100-byte body with limit 50.
	big := bytes.Repeat([]byte("x"), 100)
	_, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(big), 50)
	if !errors.Is(err, ErrPackageTooLarge) {
		t.Errorf("expected ErrPackageTooLarge, got %v", err)
	}
}

func TestStage_AtLimit_OK(t *testing.T) {
	r := newTestRepo(t)
	body := makeTarGz(t, map[string]string{"manifest.yaml": "x"})
	// Limit exactly equals body length — must succeed.
	_, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Errorf("expected ok at exact limit, got %v", err)
	}
}

func TestExtract_WritesFiles(t *testing.T) {
	r := newTestRepo(t)
	body := makeTarGz(t, map[string]string{
		"manifest.yaml":     "schema_version: 1\n",
		"README.md":         "# Hello\n",
		"bin/linux-amd64/x": "binary\n",
	})
	res, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(body), 10*1024*1024)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	paths, err := r.Extract(res.StagingDir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]bool{
		"manifest.yaml": true, "README.md": true, "bin/linux-amd64/x": true,
	}
	got := make(map[string]bool, len(paths))
	for _, p := range paths {
		got[p] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing %q in extracted paths %+v", w, paths)
		}
	}
	// Content must be readable.
	content, err := os.ReadFile(filepath.Join(res.StagingDir, "extracted", "README.md"))
	if err != nil {
		t.Fatalf("read extracted README: %v", err)
	}
	if string(content) != "# Hello\n" {
		t.Errorf("README content = %q", content)
	}
}

func TestExtract_RejectsZipSlip(t *testing.T) {
	r := newTestRepo(t)
	// Build a tar.gz with one entry whose name escapes via "../".
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "../escape.txt",
		Size:     4,
		Typeflag: tar.TypeReg,
		Mode:     0o644,
	}); err != nil {
		t.Fatalf("header: %v", err)
	}
	tw.Write([]byte("evil"))
	tw.Close()
	gw.Close()

	res, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(buf.Bytes()), 10*1024*1024)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := r.Extract(res.StagingDir); err == nil {
		t.Errorf("Extract must reject zip-slip, got nil")
	}
	// Verify nothing escaped outside extracted/.
	escapePath := filepath.Join(r.Root(), "escape.txt")
	if _, err := os.Stat(escapePath); !os.IsNotExist(err) {
		t.Errorf("zip-slip wrote outside extracted/: %s exists", escapePath)
	}
}

// stageExtractPromote is the canonical 3-step happy-path sequence used
// by tests that need a fully landed release. Production upload handler
// follows the same shape: Stage bytes → Extract to validate manifest →
// Promote once DB tx commits.
func stageExtractPromote(t *testing.T, r *Repo, appID, version string, files map[string]string) string {
	t.Helper()
	body := makeTarGz(t, files)
	res, err := r.Stage(appID, version, bytes.NewReader(body), 64*1024)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := r.Extract(res.StagingDir); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	final, err := r.Promote(res.StagingDir, appID, version)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	return final
}

func TestPromote_MovesStagingToFinal(t *testing.T) {
	r := newTestRepo(t)
	final := stageExtractPromote(t, r, "com.foo", "1.0.0", map[string]string{"manifest.yaml": "x"})
	if !r.ReleaseExists("com.foo", "1.0.0") {
		t.Errorf("release not found after promote")
	}
	if _, err := os.Stat(filepath.Join(final, "extracted")); err != nil {
		t.Errorf("extracted/ missing in final: %v", err)
	}
}

func TestPromote_OverwritesExisting(t *testing.T) {
	r := newTestRepo(t)
	stageExtractPromote(t, r, "com.foo", "1.0.0", map[string]string{"manifest.yaml": "first"})
	// Second upload to same (app_id, version).
	stageExtractPromote(t, r, "com.foo", "1.0.0", map[string]string{
		"manifest.yaml": "second",
		"NEW.md":        "y",
	})
	if !r.ReleaseExists("com.foo", "1.0.0") {
		t.Fatalf("release missing after overwrite")
	}
	pi, err := r.PackageInfo("com.foo", "1.0.0")
	if err != nil {
		t.Fatalf("PackageInfo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(pi.Path), "extracted", "NEW.md")); err != nil {
		t.Errorf("NEW.md missing after overwrite: %v", err)
	}
}

func TestDeleteRelease_Idempotent(t *testing.T) {
	r := newTestRepo(t)
	if err := r.DeleteRelease("com.foo", "1.0.0"); err != nil {
		t.Errorf("DeleteRelease on absent dir must be no-op, got %v", err)
	}
	body := makeTarGz(t, map[string]string{"manifest.yaml": "x"})
	res, _ := r.Stage("com.foo", "1.0.0", bytes.NewReader(body), 1024)
	r.Promote(res.StagingDir, "com.foo", "1.0.0")
	if err := r.DeleteRelease("com.foo", "1.0.0"); err != nil {
		t.Errorf("DeleteRelease: %v", err)
	}
	if r.ReleaseExists("com.foo", "1.0.0") {
		t.Errorf("release still present after delete")
	}
}

func TestOpenFile_RejectsTraversal(t *testing.T) {
	r := newTestRepo(t)
	stageExtractPromote(t, r, "com.foo", "1.0.0", map[string]string{"README.md": "ok"})
	for _, p := range []string{
		"../escape",
		"/etc/passwd",
		"./README.md",
		"a/../README.md",
		`README\md`,
		"",
	} {
		if _, _, err := r.OpenFile("com.foo", "1.0.0", p); err == nil {
			t.Errorf("OpenFile(%q) must reject, got nil", p)
		}
	}
}

func TestOpenFile_Streaming(t *testing.T) {
	r := newTestRepo(t)
	want := strings.Repeat("a", 4096)
	stageExtractPromote(t, r, "com.foo", "1.0.0", map[string]string{"data.bin": want})

	f, size, err := r.OpenFile("com.foo", "1.0.0", "data.bin")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()
	if size != int64(len(want)) {
		t.Errorf("size=%d, want %d", size, len(want))
	}
	got, _ := io.ReadAll(f)
	if string(got) != want {
		t.Errorf("streamed content mismatch: got len=%d want len=%d", len(got), len(want))
	}
}

func TestListExtracted(t *testing.T) {
	r := newTestRepo(t)
	stageExtractPromote(t, r, "com.foo", "1.0.0", map[string]string{
		"manifest.yaml":     "x",
		"icon.png":          "y",
		"screenshots/1.png": "z",
		"screenshots/2.png": "w",
		"bin/x":             "b",
	})
	paths, err := r.ListExtracted("com.foo", "1.0.0")
	if err != nil {
		t.Fatalf("ListExtracted: %v", err)
	}
	if len(paths) != 5 {
		t.Errorf("expected 5 entries, got %d (%+v)", len(paths), paths)
	}
}

func TestSanitizeJoin(t *testing.T) {
	root := "/tmp/r"
	cases := []struct {
		rel     string
		wantErr bool
	}{
		{"file.txt", false},
		{"a/b/c", false},
		{"", true},
		{".", true},
		{"..", true},
		{"a/../b", true},
		{"./a", true},
		{"/abs", true},
		{`back\slash`, true},
		{"a//b", true},
		{"a/./b", true},
	}
	for _, c := range cases {
		_, err := sanitizeJoin(root, c.rel)
		if c.wantErr && err == nil {
			t.Errorf("sanitizeJoin(%q) expected error", c.rel)
		}
		if !c.wantErr && err != nil {
			t.Errorf("sanitizeJoin(%q) unexpected error: %v", c.rel, err)
		}
	}
}

// TestExtract_RejectsNonRegularEntries covers L6d: symlinks, hard links,
// char/block devices, and fifos are silently skipped. The security
// relevance is that symlinks could otherwise point outside extracted/ and
// lure a later write into following them (TOCTOU-style escape). Skipping
// non-TypeReg/TypeDir entries closes that surface entirely.
func TestExtract_RejectsNonRegularEntries(t *testing.T) {
	r := newTestRepo(t)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	entries := []struct {
		name     string
		typeflag byte
		link     string
	}{
		{"manifest.yaml", tar.TypeReg, ""},
		{"README.md", tar.TypeReg, ""},
		{"escape-symlink", tar.TypeSymlink, "../../etc/passwd"},
		{"hardlink-to-readme", tar.TypeLink, "README.md"},
		{"chardev", tar.TypeChar, ""},
		{"blockdev", tar.TypeBlock, ""},
		{"fifo", tar.TypeFifo, ""},
	}
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Linkname: e.link,
			Mode:     0o644,
		}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len("data\n"))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("header %s: %v", e.name, err)
		}
		if e.typeflag == tar.TypeReg {
			tw.Write([]byte("data\n"))
		}
	}
	tw.Close()
	gw.Close()

	res, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(buf.Bytes()), 64*1024)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	paths, err := r.Extract(res.StagingDir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got := make(map[string]bool, len(paths))
	for _, p := range paths {
		got[p] = true
	}
	// Only the two TypeReg entries should be on disk.
	if !got["manifest.yaml"] || !got["README.md"] {
		t.Errorf("expected the two regular files, got %+v", paths)
	}
	for _, bad := range []string{
		"escape-symlink", "hardlink-to-readme",
		"chardev", "blockdev", "fifo",
	} {
		if got[bad] {
			t.Errorf("non-regular entry %q must be skipped, not extracted", bad)
		}
		if _, err := os.Stat(filepath.Join(res.StagingDir, "extracted", bad)); err == nil {
			t.Errorf("non-regular entry %q must not exist on disk", bad)
		}
	}
	// The symlink target outside extracted/ must NOT have been created.
	if _, err := os.Stat(filepath.Join(r.Root(), "etc", "passwd")); err == nil {
		t.Errorf("symlink escape created a file outside extracted/")
	}
}

// TestExtract_RejectsOversizedFile covers the per-file cap (M5). The
// declared header size is above MaxExtractedPerFile, so Extract must
// refuse without writing anything.
func TestExtract_RejectsOversizedFile(t *testing.T) {
	r := newTestRepo(t)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "huge.bin",
		Typeflag: tar.TypeReg,
		Size:     MaxExtractedPerFile + 1,
		Mode:     0o644,
	}); err != nil {
		t.Fatalf("header: %v", err)
	}
	tw.Close()
	gw.Close()

	res, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(buf.Bytes()), 64*1024)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	_, err = r.Extract(res.StagingDir)
	if !errors.Is(err, ErrExtractLimitExceeded) {
		t.Errorf("expected ErrExtractLimitExceeded, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.StagingDir, "extracted", "huge.bin")); err == nil {
		t.Errorf("oversized file must not be written to disk")
	}
}

// TestExtract_RejectsTooManyFiles covers the file-count cap (M5). The
// tar.gz declares more than MaxExtractedFileCount entries; Extract must
// stop and surface ErrExtractLimitExceeded.
func TestExtract_RejectsTooManyFiles(t *testing.T) {
	// Build a tar.gz with MaxExtractedFileCount+10 tiny entries.
	r := newTestRepo(t)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for i := 0; i < MaxExtractedFileCount+10; i++ {
		name := fmt.Sprintf("f%d", i)
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Size:     1,
			Mode:     0o644,
		}); err != nil {
			t.Fatalf("header %s: %v", name, err)
		}
		tw.Write([]byte("x"))
	}
	tw.Close()
	gw.Close()

	res, err := r.Stage("com.foo", "1.0.0", bytes.NewReader(buf.Bytes()), 64*1024*1024)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	_, err = r.Extract(res.StagingDir)
	if !errors.Is(err, ErrExtractLimitExceeded) {
		t.Errorf("expected ErrExtractLimitExceeded for too many files, got %v", err)
	}
}

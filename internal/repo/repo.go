// Package repo owns the on-disk layout for uploaded app packages.
// Layout: {root}/{app_id}/{version}/{package.tar.gz, package.sha256, extracted/...}
// Staging uploads land in {root}/_staging/{app_id}/{version}/ so concurrent
// uploads for different (app_id, version) pairs don't collide.
//
// All "read file from extracted/" methods route through sanitizeJoin, which
// rejects absolute paths, "..", backslashes, and Windows drive letters. The
// guard runs again after filepath.Join to catch any platform-specific
// separator tricks.
package repo

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrPackageTooLarge is returned by Stage when the uploaded bytes exceed
// the caller-supplied limit.
var ErrPackageTooLarge = errors.New("repo: package too large")

// ErrExtractLimitExceeded is returned by Extract when the tar.gz contents
// exceed the per-file size, total file count, or total extracted byte cap.
// All three guards are needed because a malicious tar can declare huge
// sparse headers, many small entries, or use gzip compression bombs.
var ErrExtractLimitExceeded = errors.New("repo: extracted contents exceed safety cap")

// Extract safety caps. Chosen to comfortably accommodate legitimate app
// packages (cf. typical Electron app 100-300 MB unpacked) while preventing
// the most common tar abuse patterns: zip bombs, million-entry tables,
// and oversized individual binaries.
const (
	// MaxExtractedPerFile caps a single extracted file. 256 MB is large
	// enough for any plausible single binary but stops a "10 GB asset"
	// attack.
	MaxExtractedPerFile = 256 << 20
	// MaxExtractedFileCount caps total entries. 5000 is far beyond any
	// legitimate app package (typical: 10-200 entries) but stops the
	// "million tiny files" inode-exhaustion attack.
	MaxExtractedFileCount = 5000
	// MaxExtractedTotalBytes caps the sum of all extracted file sizes.
	// 2 GB is well above a typical app package while still bounding disk
	// usage against gzip compression bombs (a 200 MB input can decompress
	// to tens of GB without this guard).
	MaxExtractedTotalBytes = 2 << 30
)

// Repo is the file-system store for uploaded packages.
type Repo struct {
	root string
}

// New constructs a Repo rooted at root, creating the directory if needed.
func New(root string) (*Repo, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create repo root %s: %w", root, err)
	}
	return &Repo{root: root}, nil
}

// Root returns the absolute path of the repo root. Exposed so callers
// (tests, tooling) can inspect layout. Production HTTP handlers should
// never send this to clients.
func (r *Repo) Root() string { return r.root }

// StageResult is returned by Stage: where bytes landed, the sha256 hex
// of the bytes received, and the total size in bytes.
type StageResult struct {
	StagingDir string
	SHA256Hex  string
	Size       int64
}

// Stage streams an uploaded tar.gz into a staging directory:
//
//	{root}/_staging/{app_id}/{uploadID}/package.tar.gz
//	{root}/_staging/{app_id}/{uploadID}/package.sha256
//
// where {uploadID} is a caller-supplied unique token (auth.NewID()) so two
// concurrent uploads for different versions of the same app do not collide.
// Caller validates the manifest, then Promote()s the staging directory to
// its final location. On any failure caller must Discard(stagingDir).
//
// At entry, Stage wipes the entire staging subtree for this {uploadID} so a
// previously failed retry does not leak into the new attempt. It deliberately
// does NOT touch sibling {uploadID} directories of the same app.
func (r *Repo) Stage(appID, uploadID string, body io.Reader, maxBytes int64) (StageResult, error) {
	staging := r.stagingPath(appID, uploadID)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return StageResult{}, fmt.Errorf("mkdir staging: %w", err)
	}
	pkgPath := filepath.Join(staging, "package.tar.gz")

	f, err := os.Create(pkgPath)
	if err != nil {
		return StageResult{}, fmt.Errorf("create package file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	// Read at most maxBytes+1 so we can distinguish "exactly at limit"
	// from "over limit".
	lr := io.LimitReader(body, maxBytes+1)
	n, err := io.Copy(io.MultiWriter(f, h), lr)
	if err != nil {
		_ = os.RemoveAll(staging)
		return StageResult{}, fmt.Errorf("write package: %w", err)
	}
	if n > maxBytes {
		_ = os.RemoveAll(staging)
		return StageResult{}, ErrPackageTooLarge
	}
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(staging)
		return StageResult{}, fmt.Errorf("close package file: %w", err)
	}
	shaHex := hex.EncodeToString(h.Sum(nil))
	if err := os.WriteFile(filepath.Join(staging, "package.sha256"), []byte(shaHex+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(staging)
		return StageResult{}, fmt.Errorf("write sha256 sidecar: %w", err)
	}
	return StageResult{StagingDir: staging, SHA256Hex: shaHex, Size: n}, nil
}

// Extract decompresses package.tar.gz inside stagingDir into extracted/.
// Returns the list of regular files written, each relative to extracted/.
//
// Symlinks, device nodes, fifos and other non-regular entries are silently
// skipped — release packages should never carry them, and dropping them
// avoids whole categories of zip-slip / symlink-escape attacks.
//
// Three safety caps protect against malicious tar.gz payloads (see
// MaxExtractedPerFile / MaxExtractedFileCount / MaxExtractedTotalBytes).
// Exceeding any cap returns ErrExtractLimitExceeded and the caller must
// Discard the staging dir.
func (r *Repo) Extract(stagingDir string) ([]string, error) {
	pkgPath := filepath.Join(stagingDir, "package.tar.gz")
	extractedRoot := filepath.Join(stagingDir, "extracted")
	_ = os.RemoveAll(extractedRoot)
	if err := os.MkdirAll(extractedRoot, 0o700); err != nil {
		return nil, err
	}
	f, err := os.Open(pkgPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var paths []string
	var totalBytes int64
	fileCount := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "" || strings.HasPrefix(name, "/") {
			continue
		}
		target, err := sanitizeJoin(extractedRoot, name)
		if err != nil {
			return nil, fmt.Errorf("unsafe path %q: %w", hdr.Name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			// Per-file size cap. hdr.Size is the declared entry size; the
			// underlying tar.Reader will return io.ErrUnexpectedEOF if the
			// stream is shorter, so trusting hdr.Size here is safe.
			if hdr.Size > MaxExtractedPerFile {
				return nil, fmt.Errorf("%w: file %q declared %d bytes (max %d)",
					ErrExtractLimitExceeded, hdr.Name, hdr.Size, MaxExtractedPerFile)
			}
			if totalBytes+hdr.Size > MaxExtractedTotalBytes {
				return nil, fmt.Errorf("%w: total extracted bytes would exceed %d",
					ErrExtractLimitExceeded, MaxExtractedTotalBytes)
			}
			fileCount++
			if fileCount > MaxExtractedFileCount {
				return nil, fmt.Errorf("%w: file count exceeds %d",
					ErrExtractLimitExceeded, MaxExtractedFileCount)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return nil, err
			}
			of, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, err
			}
			// Cap the actual copy as a defense-in-depth: even if hdr.Size
			// is bounded, a maliciously crafted tar with a corrupt size
			// field could otherwise stream unbounded data through tr.
			n, err := io.Copy(of, io.LimitReader(tr, MaxExtractedPerFile+1))
			of.Close()
			if err != nil {
				return nil, err
			}
			if n > MaxExtractedPerFile {
				return nil, fmt.Errorf("%w: file %q exceeded %d bytes on disk",
					ErrExtractLimitExceeded, hdr.Name, MaxExtractedPerFile)
			}
			totalBytes += n
			rel, _ := filepath.Rel(extractedRoot, target)
			paths = append(paths, filepath.ToSlash(rel))
		}
	}
	return paths, nil
}

// Promote atomically (on POSIX; best-effort on Windows) moves the staging
// directory to its final location:
//
//	{root}/{app_id}/{version}/
//
// If the destination already exists (overwrite), it's renamed to {final}.old
// first and deleted after the staging→final rename succeeds. A crash between
// the two renames leaves {final}.old behind; the next Promote cleans it up.
func (r *Repo) Promote(stagingDir, appID, version string) (string, error) {
	final := r.releasePath(appID, version)
	// Ensure the final parent exists — Windows rename refuses to create
	// intermediate parents and returns "The system cannot find the path
	// specified." POSIX rename inside the same filesystem doesn't need this.
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return "", fmt.Errorf("mkdir final parent: %w", err)
	}
	old := final + ".old"
	if _, err := os.Stat(final); err == nil {
		_ = os.Rename(final, old)
	}
	if err := os.Rename(stagingDir, final); err != nil {
		// Try to roll back so final still points at the previous release.
		// If roll-back also fails, final is gone but .old must still be
		// removed — otherwise it sits on disk forever: the next Promote
		// for this (app_id, version) sees final missing and skips the
		// .old dance, so nothing revisits the orphan.
		if _, statErr := os.Stat(old); statErr == nil {
			if rerr := os.Rename(old, final); rerr != nil {
				_ = os.RemoveAll(old)
			}
		}
		return "", fmt.Errorf("promote staging to final: %w", err)
	}
	_ = os.RemoveAll(old)
	return final, nil
}

// Discard removes a staging directory after a failed parse or validation.
func (r *Repo) Discard(stagingDir string) {
	_ = os.RemoveAll(stagingDir)
}

// DeleteRelease removes {root}/{app_id}/{version}/ entirely.
// Idempotent — returns nil if the directory didn't exist.
func (r *Repo) DeleteRelease(appID, version string) error {
	if err := os.RemoveAll(r.releasePath(appID, version)); err != nil {
		return fmt.Errorf("delete release %s/%s: %w", appID, version, err)
	}
	return nil
}

// DeleteApp removes the entire {root}/{app_id}/ tree. Used by AdminDeleteApp
// to clean up the file system after the DB's ON DELETE CASCADE has wiped all
// release rows — CASCADE cannot reach the file system. Idempotent: returns
// nil if the directory didn't exist.
//
// appID safety: callers validate appID via appIDRegex (reverse-domain,
// [a-z0-9-.] only, no path separators) before reaching here, so
// filepath.Join cannot escape {root}.
func (r *Repo) DeleteApp(appID string) error {
	if err := os.RemoveAll(filepath.Join(r.root, appID)); err != nil {
		return fmt.Errorf("delete app %s: %w", appID, err)
	}
	return nil
}

// ReleaseExists reports whether {root}/{app_id}/{version}/ is present.
func (r *Repo) ReleaseExists(appID, version string) bool {
	_, err := os.Stat(r.releasePath(appID, version))
	return err == nil
}

// OpenFile opens a file inside the release's extracted/ tree, returning
// an *os.File for streaming plus its size in bytes. Caller must Close.
// Returns os.ErrNotExist if the file is absent.
func (r *Repo) OpenFile(appID, version, relPath string) (*os.File, int64, error) {
	extractedRoot := filepath.Join(r.releasePath(appID, version), "extracted")
	target, err := sanitizeJoin(extractedRoot, relPath)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, 0, err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if stat.IsDir() {
		f.Close()
		return nil, 0, fmt.Errorf("path is a directory: %s", relPath)
	}
	return f, stat.Size(), nil
}

// PackageInfo describes the stored package on disk.
type PackageInfo struct {
	Path   string
	SHA256 string
	Size   int64
}

// PackageInfo reads the staged / promoted package's path, sha256 (from the
// sidecar), and size. Returns os.ErrNotExist if the release dir or any of
// the canonical files is absent. A missing sidecar is treated as corruption
// — dev handler writes both atomically, so absence means data loss.
func (r *Repo) PackageInfo(appID, version string) (PackageInfo, error) {
	final := r.releasePath(appID, version)
	pkgPath := filepath.Join(final, "package.tar.gz")
	stat, err := os.Stat(pkgPath)
	if err != nil {
		return PackageInfo{}, err
	}
	shaHex, err := os.ReadFile(filepath.Join(final, "package.sha256"))
	if err != nil {
		return PackageInfo{}, fmt.Errorf("read sha256 sidecar for %s/%s: %w", appID, version, err)
	}
	return PackageInfo{
		Path:   pkgPath,
		Size:   stat.Size(),
		SHA256: strings.TrimSpace(string(shaHex)),
	}, nil
}

// ListExtracted returns the relative paths of every regular file under
// the release's extracted/ tree. Used during upload to record the asset
// manifest (icons, screenshots, docs).
func (r *Repo) ListExtracted(appID, version string) ([]string, error) {
	extractedRoot := filepath.Join(r.releasePath(appID, version), "extracted")
	var out []string
	err := filepath.WalkDir(extractedRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(extractedRoot, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

// ReleaseRef names one {root}/{app_id}/{version}/ directory on disk.
type ReleaseRef struct {
	AppID   string
	Version string
	Path    string
}

// ListReleaseDirs walks {root} top-down and yields every release directory
// as a ReleaseRef. _staging/ is skipped — those are in-flight uploads.
// Directly-named files at any level are skipped (release dirs are always
// two levels deep: {root}/{app_id}/{version}/).
//
// Used at server startup to diff against app_releases: any ReleaseRef whose
// (app_id, version) has no DB row is an orphan left by a crashed Promote or
// an out-of-band deletion, and the caller should DeleteRelease it.
func (r *Repo) ListReleaseDirs() ([]ReleaseRef, error) {
	var out []ReleaseRef
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil, err
	}
	for _, appEntry := range entries {
		if !appEntry.IsDir() {
			continue
		}
		appID := appEntry.Name()
		if appID == "_staging" {
			continue
		}
		appDir := filepath.Join(r.root, appID)
		versionEntries, err := os.ReadDir(appDir)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", appDir, err)
		}
		for _, vEntry := range versionEntries {
			if !vEntry.IsDir() {
				continue
			}
			out = append(out, ReleaseRef{
				AppID:   appID,
				Version: vEntry.Name(),
				Path:    filepath.Join(appDir, vEntry.Name()),
			})
		}
	}
	return out, nil
}

func (r *Repo) stagingPath(appID, uploadID string) string {
	return filepath.Join(r.root, "_staging", appID, uploadID)
}

func (r *Repo) releasePath(appID, version string) string {
	return filepath.Join(r.root, appID, version)
}

// sanitizeJoin joins root and rel, refusing absolute paths, any ".."
// segment, backslashes, and Windows drive letters. The post-Join prefix
// check is the final guard against platform-specific separator tricks.
//
// Backslash rejection happens BEFORE ToSlash so that a Windows path like
// "bin\evil" is caught even though ToSlash would otherwise normalize the
// backslash to "/".
func sanitizeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.Contains(rel, "\\") {
		return "", fmt.Errorf("backslash in path")
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("absolute path")
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid segment %q in %q", seg, rel)
		}
	}
	cleanedRoot := filepath.Clean(root)
	cleanedAbs := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if cleanedAbs != cleanedRoot && !strings.HasPrefix(cleanedAbs, cleanedRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes root: %q", rel)
	}
	return cleanedAbs, nil
}

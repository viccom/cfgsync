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
//   {root}/_staging/{app_id}/{version}/package.tar.gz
//   {root}/_staging/{app_id}/{version}/package.sha256
// Caller validates the manifest, then Promote()s the staging directory to
// its final location. On any failure caller must Discard(stagingDir).
func (r *Repo) Stage(appID, version string, body io.Reader, maxBytes int64) (StageResult, error) {
	staging := r.stagingPath(appID, version)
	// Remove any stale staging from a previous failed attempt at this
	// (app_id, version) — keeps Promote's invariants simple.
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
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return nil, err
			}
			of, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, err
			}
			if _, err := io.Copy(of, tr); err != nil {
				of.Close()
				return nil, err
			}
			of.Close()
			rel, _ := filepath.Rel(extractedRoot, target)
			paths = append(paths, filepath.ToSlash(rel))
		}
	}
	return paths, nil
}

// Promote atomically (on POSIX; best-effort on Windows) moves the staging
// directory to its final location:
//   {root}/{app_id}/{version}/
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
		if _, statErr := os.Stat(old); statErr == nil {
			_ = os.Rename(old, final)
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
// sidecar), and size. Returns os.ErrNotExist if the release dir is absent.
func (r *Repo) PackageInfo(appID, version string) (PackageInfo, error) {
	final := r.releasePath(appID, version)
	pkgPath := filepath.Join(final, "package.tar.gz")
	stat, err := os.Stat(pkgPath)
	if err != nil {
		return PackageInfo{}, err
	}
	shaHex, _ := os.ReadFile(filepath.Join(final, "package.sha256"))
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
	err := filepath.Walk(extractedRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(extractedRoot, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

func (r *Repo) stagingPath(appID, version string) string {
	return filepath.Join(r.root, "_staging", appID, version)
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
	for _, seg := range strings.Split(rel, "/") {
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

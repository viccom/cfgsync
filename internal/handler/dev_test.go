package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// makePkg builds an in-memory tar.gz from name→content. Same shape as
// repo_test's helper, duplicated because cross-package test helpers
// aren't a thing.
func makePkg(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
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
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar body: %v", err)
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

// makeUploadBody wraps the tar.gz bytes in a multipart/form-data body
// with a single "package" field. Returns (body, contentType).
func makeUploadBody(t *testing.T, tarGz []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("package", "x.tar.gz")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(tarGz); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return body, mw.FormDataContentType()
}

// uploadReq dispatches a multipart upload through h. The path's {app_id}
// and optional {version} are set via SetPathValue so the dev handler
// reads them the same way Go 1.22's mux would populate them.
func uploadReq(t *testing.T, h http.Handler, method, path, appID, version, token string, tarGz []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := makeUploadBody(t, tarGz)
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", ct)
	req.SetPathValue("app_id", appID)
	if version != "" {
		req.SetPathValue("version", version)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const validManifestYAML = `schema_version: 1
version: "1.0.0"
display_name: "Test"
description: "for testing"
summary: "card text"
tags: ["test", "demo"]
keywords: ["e2e"]
author:
  name: "Jane"
license: "MIT"
homepage: "https://example.com"
requires_os: ["linux"]
platforms:
  linux-amd64:
    path: bin/myapp
`

// validPkg bundles manifest.yaml + README.md + binary into a tar.gz.
func validPkg(t *testing.T, version string) []byte {
	t.Helper()
	manifest := validManifestYAML
	if version != "" {
		manifest = strings.Replace(manifest, `version: "1.0.0"`, `version: "`+version+`"`, 1)
	}
	return makePkg(t, map[string]string{
		"manifest.yaml": manifest,
		"README.md":     "# Test\n",
		"bin/myapp":     "binary content",
	})
}

// --- UploadRelease (POST) ---

func TestUploadRelease_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// DB row must exist.
	var count int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_releases WHERE app_id = 'com.foo'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 release row, got %d", count)
	}
	// apps cache fields must be bumped.
	var latest, summary, visibility string
	env.db.QueryRow(`SELECT latest_version, summary, visibility FROM apps WHERE app_id = 'com.foo'`).
		Scan(&latest, &summary, &visibility)
	if latest != "1.0.0" {
		t.Errorf("latest_version=%q, want 1.0.0", latest)
	}
	if summary != "card text" {
		t.Errorf("summary=%q, want 'card text'", summary)
	}
	if visibility != "public" {
		t.Errorf("visibility=%q", visibility)
	}
	// Tags synced.
	var tagCount int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tags WHERE app_id = 'com.foo'`).Scan(&tagCount)
	if tagCount != 2 {
		t.Errorf("expected 2 tags, got %d", tagCount)
	}
	// FS promotion succeeded.
	if !env.repo.ReleaseExists("com.foo", "1.0.0") {
		t.Errorf("release dir missing in repo")
	}
}

func TestUploadRelease_RejectsNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, ""))
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", w.Code)
	}
}

func TestUploadRelease_AppNotFound(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/nope/releases", "nope", "", tok, validPkg(t, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestUploadRelease_MissingREADME(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	pkg := makePkg(t, map[string]string{
		"manifest.yaml": validManifestYAML,
		"bin/myapp":     "binary",
	})
	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, pkg)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing README, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "readme_required") {
		t.Errorf("expected readme_required code, got %s", w.Body.String())
	}
}

func TestUploadRelease_InvalidManifest(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	// Missing display_name + bad version.
	bad := `schema_version: 1
version: "not-a-version"
`
	pkg := makePkg(t, map[string]string{
		"manifest.yaml": bad,
		"README.md":     "x",
	})
	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, pkg)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_manifest") {
		t.Errorf("expected invalid_manifest code, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "display_name") {
		t.Errorf("expected display_name field error, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "version") {
		t.Errorf("expected version field error, got %s", w.Body.String())
	}
}

func TestUploadRelease_VersionExists_409(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	if w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, "")); w.Code != http.StatusOK {
		t.Fatalf("first upload: status=%d body=%s", w.Code, w.Body.String())
	}
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 on duplicate, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "version_exists") {
		t.Errorf("expected version_exists code, got %s", w.Body.String())
	}
}

func TestUploadRelease_TooLarge(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.MaxPackageBytes = 200 // tight cap for the test
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	// 1 MB body blows past the 200-byte cap.
	bigPkg := makePkg(t, map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
		"blob":          strings.Repeat("x", 1024),
	})
	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, bigPkg)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
}

// --- OverwriteRelease (PUT) ---

func TestOverwriteRelease_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	// First upload.
	post := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	if w := uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, "")); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}

	// Overwrite same version. Manifest supplies README + new INSTALL.
	pkg2 := makePkg(t, map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "# Updated\n",
		"INSTALL.md":    "Install instructions\n",
		"bin/myapp":     "new binary",
	})
	put := adminChain(env, OverwriteRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, put, "PUT", "/api/v1/dev/apps/com.foo/releases/1.0.0", "com.foo", "1.0.0", tok, pkg2)
	if w.Code != http.StatusOK {
		t.Fatalf("overwrite: status=%d body=%s", w.Code, w.Body.String())
	}
	// Still exactly 1 release row (the INSERT replaced).
	var count int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_releases WHERE app_id = 'com.foo' AND version = '1.0.0'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after overwrite, got %d", count)
	}
}

func TestOverwriteRelease_VersionMismatch(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	// Manifest says 1.0.0, URL says 2.0.0.
	h := adminChain(env, OverwriteRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "PUT", "/api/v1/dev/apps/com.foo/releases/2.0.0", "com.foo", "2.0.0", tok, validPkg(t, ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "version_mismatch") {
		t.Errorf("expected version_mismatch, got %s", w.Body.String())
	}
}

// --- ListDevReleases (GET) ---

func TestListDevReleases_NewestFirst(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	post := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, "1.0.0"))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, "1.2.0"))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, "1.10.0"))

	// GET list — must order 1.10.0 > 1.2.0 > 1.0.0 (semver, not lexical).
	h := adminChain(env, ListDevReleases(env.db))
	req := httptest.NewRequest("GET", "/api/v1/dev/apps/com.foo/releases", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Releases []struct {
			Version string `json:"version"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(resp.Releases) != 3 {
		t.Fatalf("expected 3 releases, got %d", len(resp.Releases))
	}
	got := []string{resp.Releases[0].Version, resp.Releases[1].Version, resp.Releases[2].Version}
	want := []string{"1.10.0", "1.2.0", "1.0.0"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("position %d: got %s, want %s (full order %+v)", i, got[i], want[i], got)
		}
	}
}

// --- DeleteRelease (DELETE) ---

func TestDeleteRelease_Success_RecalculatesLatest(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	post := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, "1.0.0"))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, "2.0.0"))

	// Delete the newer one — latest_version must fall back to 1.0.0.
	del := adminChain(env, DeleteRelease(env.db, env.repo))
	req := httptest.NewRequest("DELETE", "/api/v1/dev/apps/com.foo/releases/2.0.0", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "2.0.0")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	del.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}

	var latest string
	env.db.QueryRow(`SELECT latest_version FROM apps WHERE app_id = 'com.foo'`).Scan(&latest)
	if latest != "1.0.0" {
		t.Errorf("latest_version=%q, want 1.0.0", latest)
	}
	if env.repo.ReleaseExists("com.foo", "2.0.0") {
		t.Errorf("FS dir for 2.0.0 still present after delete")
	}
}

func TestDeleteRelease_NotFound(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	del := adminChain(env, DeleteRelease(env.db, env.repo))
	req := httptest.NewRequest("DELETE", "/api/v1/dev/apps/com.foo/releases/9.9.9", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "9.9.9")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	del.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestCompensatePromoteFailure_OverwriteLatestPreservesConsistency covers the
// exact scenario N1 was filed for: PUT overwrites version X which is also the
// current latest. A naive "restore prevLatest=X" compensation would re-point
// apps.latest_version at a row we just deleted. pickLatestVersion(remaining)
// must instead pick the next-newest surviving release.
func TestCompensatePromoteFailure_OverwriteLatestPreservesConsistency(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	// Seed two releases: 1.0.0 (older) and 2.0.0 (latest). apps.latest_version
	// points at 2.0.0 — the row that the failing PUT would have replaced.
	seedReleaseRow(t, env, "com.foo", "1.0.0", adminUID)
	seedReleaseRow(t, env, "com.foo", "2.0.0", adminUID)
	if _, err := env.db.Exec(
		`UPDATE apps SET latest_version = '2.0.0' WHERE app_id = 'com.foo'`,
	); err != nil {
		t.Fatalf("seed latest: %v", err)
	}

	// Simulate the post-commit Promote failure path: the new 2.0.0 row is
	// already in place (in real life the tx just committed), and now we
	// compensate.
	compensatePromoteFailure(env.db, "com.foo", "2.0.0")

	var latest string
	if err := env.db.QueryRow(
		`SELECT latest_version FROM apps WHERE app_id = 'com.foo'`,
	).Scan(&latest); err != nil {
		t.Fatalf("scan latest: %v", err)
	}
	if latest != "1.0.0" {
		t.Errorf("latest_version=%q, want 1.0.0 (must point at a surviving release)", latest)
	}

	// 2.0.0 row must be gone; 1.0.0 must survive.
	var n int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_releases WHERE app_id='com.foo' AND version='2.0.0'`).Scan(&n)
	if n != 0 {
		t.Errorf("2.0.0 row still present after compensation")
	}
	env.db.QueryRow(`SELECT COUNT(*) FROM app_releases WHERE app_id='com.foo' AND version='1.0.0'`).Scan(&n)
	if n != 1 {
		t.Errorf("1.0.0 row lost during compensation")
	}
}

// TestCompensatePromoteFailure_LastReleaseClearsLatest covers the boundary:
// when the deleted version was the only release, apps.latest_version must
// become "" rather than pointing at a missing row.
func TestCompensatePromoteFailure_LastReleaseClearsLatest(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	seedReleaseRow(t, env, "com.foo", "1.0.0", adminUID)
	if _, err := env.db.Exec(
		`UPDATE apps SET latest_version = '1.0.0' WHERE app_id = 'com.foo'`,
	); err != nil {
		t.Fatalf("seed latest: %v", err)
	}

	compensatePromoteFailure(env.db, "com.foo", "1.0.0")

	var latest string
	if err := env.db.QueryRow(
		`SELECT latest_version FROM apps WHERE app_id = 'com.foo'`,
	).Scan(&latest); err != nil {
		t.Fatalf("scan latest: %v", err)
	}
	if latest != "" {
		t.Errorf("latest_version=%q, want empty when no releases remain", latest)
	}
}

// Smoke-test: rollback semantics. If DB tx fails after staging, the staging
// dir is cleaned and no half-state leaks.
func TestUploadRelease_DbConflictRollsBackStaging(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	// Pre-seed a release row directly so the upload INSERT collides on UNIQUE.
	seedReleaseRow(t, env, "com.foo", "1.0.0", adminUID)

	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, validPkg(t, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 conflict, got %d body=%s", w.Code, w.Body.String())
	}
	// Staging pending dir must be cleaned (the actual upload bytes).
	pendingDir := filepath.Join(env.repo.Root(), "_staging", "com.foo", "_pending")
	if _, err := os.Stat(pendingDir); !os.IsNotExist(err) {
		t.Errorf("staging pending dir %s still present after rollback (err=%v)", pendingDir, err)
	}
	// Pre-seeded release row must still be intact (no half-overwrite).
	var n int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_releases WHERE app_id='com.foo' AND version='1.0.0'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 pre-seeded row preserved, got %d", n)
	}
}

// seedReleaseRow inserts a release row directly, bypassing the upload
// pipeline. Used to set up conflict scenarios. Caller supplies a real
// admin UID so the FK on app_releases.created_by is satisfied.
func seedReleaseRow(t *testing.T, env *testEnv, appID, version, adminUID string) {
	t.Helper()
	_, err := env.db.Exec(
		`INSERT INTO app_releases (
			app_id, version, version_major, version_minor, version_patch, version_pre,
			manifest_yaml, manifest_json, package_size, package_sha256,
			docs_json, assets_json, release_notes, created_at, created_by
		) VALUES (?, ?, 1, 0, 0, '', 'x', '{}', 1, 'x', '{}', '[]', '', ?, ?)`,
		appID, version, nowUnix(), adminUID,
	)
	if err != nil {
		t.Fatalf("seed release row: %v", err)
	}
}

// TestUploadRelease_ConcurrentPOSTSameVersion_AllConflictsAre409 covers the
// race where two clients race the same (app_id, version). With
// SetMaxOpenConns(1) the SELECTs serialize, but a client that SELECTs
// before another tx commits still hits the UNIQUE constraint on INSERT.
// All non-success responses must be 409 (never 500 "internal").
func TestUploadRelease_ConcurrentPOSTSameVersion_AllConflictsAre409(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))

	const N = 8
	var wg sync.WaitGroup
	codes := make([]int, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases",
				"com.foo", "", tok, validPkg(t, ""))
			codes[idx] = w.Code
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, conflict, other int
	otherCodes := []int{}
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			other++
			otherCodes = append(otherCodes, c)
		}
	}
	if ok != 1 {
		t.Errorf("expected exactly 1 success, got %d (codes=%v)", ok, codes)
	}
	if other > 0 {
		t.Errorf("expected all failures to be 409, got %d other=%v (codes=%v)", other, otherCodes, codes)
	}
	if conflict != N-1 {
		t.Errorf("expected %d conflicts, got %d (codes=%v)", N-1, conflict, codes)
	}
}

// TestUploadRelease_ConcurrentPOSTDifferentVersions_AllSucceed covers H2:
// before the staging isolation fix, every Stage wiped the parent
// _staging/{app_id}/ dir, so two concurrent uploads for different versions
// of the same app would clobber each other. Now each upload uses a unique
// uploadID so siblings are untouched.
func TestUploadRelease_ConcurrentPOSTDifferentVersions_AllSucceed(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))

	versions := []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0"}
	codes := make([]int, len(versions))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, v := range versions {
		wg.Add(1)
		go func(idx int, version string) {
			defer wg.Done()
			<-start
			w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases",
				"com.foo", "", tok, validPkg(t, version))
			codes[idx] = w.Code
		}(i, v)
	}
	close(start)
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("upload %s: expected 200, got %d (all codes=%v)", versions[i], c, codes)
		}
	}
	// Each version's release directory must exist in the repo (no clobber).
	for _, v := range versions {
		if !env.repo.ReleaseExists("com.foo", v) {
			t.Errorf("release %s missing after concurrent upload", v)
		}
	}
}

// lexical ASC would order "rc.1" < "rc.11" < "rc.2", but semver §11 says
// rc.11 > rc.2 > rc.1. Verifies Go-layer sort via sortReleasesBySemverDesc.
func TestListDevReleases_PrereleaseOrdering(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	post := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	versions := []string{"1.0.0-rc.1", "1.0.0-rc.2", "1.0.0-rc.11", "1.0.0"}
	for _, v := range versions {
		if w := uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases",
			"com.foo", "", tok, validPkg(t, v)); w.Code != http.StatusOK {
			t.Fatalf("seed %s: %d %s", v, w.Code, w.Body.String())
		}
	}

	h := adminChain(env, ListDevReleases(env.db))
	req := httptest.NewRequest("GET", "/api/v1/dev/apps/com.foo/releases", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Releases []struct {
			Version string `json:"version"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := make([]string, len(resp.Releases))
	for i, r := range resp.Releases {
		got[i] = r.Version
	}
	// semver precedence (newest first): 1.0.0 > 1.0.0-rc.11 > 1.0.0-rc.2 > 1.0.0-rc.1
	want := []string{"1.0.0", "1.0.0-rc.11", "1.0.0-rc.2", "1.0.0-rc.1"}
	if len(got) != len(want) {
		t.Fatalf("expected %d releases, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %s, want %s (full: %+v)", i, got[i], want[i], got)
		}
	}
}

// TestDeleteRelease_PrereleaseLatestRecompute verifies that deleting the
// latest prerelease picks the next-highest prerelease by semver precedence,
// not the lexically-smallest pre string.
func TestDeleteRelease_PrereleaseLatestRecompute(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	post := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases",
		"com.foo", "", tok, validPkg(t, "1.0.0-rc.1"))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases",
		"com.foo", "", tok, validPkg(t, "1.0.0-rc.2"))
	uploadReq(t, post, "POST", "/api/v1/dev/apps/com.foo/releases",
		"com.foo", "", tok, validPkg(t, "1.0.0-rc.11"))

	// apps.latest_version should be rc.11 (highest prerelease).
	var latest string
	env.db.QueryRow(`SELECT latest_version FROM apps WHERE app_id='com.foo'`).Scan(&latest)
	if latest != "1.0.0-rc.11" {
		t.Fatalf("latest after upload = %q, want 1.0.0-rc.11", latest)
	}

	// Delete rc.11 → latest must fall to rc.2 (not rc.1, not lexically smallest).
	del := adminChain(env, DeleteRelease(env.db, env.repo))
	req := httptest.NewRequest("DELETE", "/api/v1/dev/apps/com.foo/releases/1.0.0-rc.11", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0-rc.11")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	del.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	env.db.QueryRow(`SELECT latest_version FROM apps WHERE app_id='com.foo'`).Scan(&latest)
	if latest != "1.0.0-rc.2" {
		t.Errorf("latest after delete rc.11 = %q, want 1.0.0-rc.2", latest)
	}
}

package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedRelease uploads a release through the dev pipeline so catalog tests
// get a real DB row + FS package. Idempotent on app_id — only seeds the
// apps row the first time so multiple version uploads for the same app
// don't trip the apps.app_id UNIQUE constraint.
func seedRelease(t *testing.T, env *testEnv, adminUID, appID string, files map[string]string) {
	t.Helper()
	var x int
	_ = env.db.QueryRow(`SELECT 1 FROM apps WHERE app_id = ?`, appID).Scan(&x)
	if x == 0 {
		env.seedApp(t, appID, "X", adminUID)
	}
	tok := env.userToken(adminUID, "admin@example.com", true)
	pkg := makePkg(t, files)
	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/"+appID+"/releases", appID, "", tok, pkg)
	if w.Code != http.StatusOK {
		t.Fatalf("seed release %s: %d %s", appID, w.Code, w.Body.String())
	}
}

func setAppVisibility(t *testing.T, env *testEnv, appID, vis string) {
	t.Helper()
	if _, err := env.db.Exec(`UPDATE apps SET visibility = ? WHERE app_id = ?`, vis, appID); err != nil {
		t.Fatalf("set visibility: %v", err)
	}
}

func doCatalogGet(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, &bytes.Buffer{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// --- ListCatalogApps ---

func TestListCatalogApps_OnlyPublicWithRelease(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)

	// public + has release → appears
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "# Foo\n",
	})
	// private → hidden
	seedRelease(t, env, admin, "com.priv", map[string]string{
		"manifest.yaml": strings.Replace(validManifestYAML, "1.0.0", "1.0.0", 1),
		"README.md":     "# Priv\n",
	})
	setAppVisibility(t, env, "com.priv", "private")

	// public + no release → hidden (latest_version is empty)
	env.seedApp(t, "com.empty", "Empty", admin)

	w := doCatalogGet(ListCatalogApps(env.db), "/api/v1/catalog/apps")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"app_id":"com.foo"`) {
		t.Errorf("expected com.foo in list, got %s", body)
	}
	if strings.Contains(body, "com.priv") {
		t.Errorf("private app must not appear: %s", body)
	}
	if strings.Contains(body, "com.empty") {
		t.Errorf("app without release must not appear: %s", body)
	}
}

func TestListCatalogApps_TagFilter(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)

	mfCli := strings.Replace(validManifestYAML, `tags: ["test", "demo"]`, `tags: ["cli"]`, 1)
	mfAi := strings.Replace(validManifestYAML, `tags: ["test", "demo"]`, `tags: ["ai"]`, 1)

	seedRelease(t, env, admin, "com.cli", map[string]string{"manifest.yaml": mfCli, "README.md": "x"})
	seedRelease(t, env, admin, "com.ai", map[string]string{"manifest.yaml": mfAi, "README.md": "x"})

	w := doCatalogGet(ListCatalogApps(env.db), "/api/v1/catalog/apps?tag=cli")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "com.cli") {
		t.Errorf("expected com.cli in tag=cli filter, got %s", body)
	}
	if strings.Contains(body, "com.ai") {
		t.Errorf("com.ai must not appear in tag=cli filter: %s", body)
	}
}

func TestListCatalogApps_QFilter(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)

	mfFoo := strings.Replace(validManifestYAML, `display_name: "Test"`, `display_name: "FooBar"`, 1)
	mfBaz := strings.Replace(validManifestYAML, `display_name: "Test"`, `display_name: "BazQux"`, 1)

	seedRelease(t, env, admin, "com.foo", map[string]string{"manifest.yaml": mfFoo, "README.md": "x"})
	seedRelease(t, env, admin, "com.baz", map[string]string{"manifest.yaml": mfBaz, "README.md": "x"})

	w := doCatalogGet(ListCatalogApps(env.db), "/api/v1/catalog/apps?q=foo")
	if !strings.Contains(w.Body.String(), "com.foo") {
		t.Errorf("q=foo should match com.foo: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "com.baz") {
		t.Errorf("q=foo should not match com.baz: %s", w.Body.String())
	}
}

// --- GetCatalogApp ---

func TestGetCatalogApp_Success(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	w := httptest.NewRecorder()
	GetCatalogApp(env.db).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"display_name":"Test"`,
		`"latest_release"`,
		`"version":"1.0.0"`,
		`"download_url"`,
		`"tags":["demo","test"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body, got %s", want, body)
		}
	}
}

// TestGetCatalogApp_LatestVersionDanglingReturnsNull covers N7: if
// apps.latest_version points at a release row that no longer exists (data
// inconsistency — e.g. a bug or out-of-band deletion), GetCatalogApp must
// emit latest_release=null and a 200, not silently return a half-empty
// metadata block. The SPA can then prompt the user to retry instead of
// rendering dead URLs.
func TestGetCatalogApp_LatestVersionDanglingReturnsNull(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", admin)
	// Point latest_version at a release that was never inserted.
	if _, err := env.db.Exec(
		`UPDATE apps SET latest_version = '9.9.9' WHERE app_id = 'com.foo'`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	w := httptest.NewRecorder()
	GetCatalogApp(env.db).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, `"latest_release":{`) {
		t.Errorf("expected latest_release to be absent/null, got %s", body)
	}
	// Must NOT contain a download_url — the SPA would otherwise render a
	// link that 404s on click.
	if strings.Contains(body, `"download_url"`) {
		t.Errorf("response leaked download_url for dangling latest: %s", body)
	}
}

func TestGetCatalogApp_PrivateNotFound(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
	})
	setAppVisibility(t, env, "com.foo", "private")

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	w := httptest.NewRecorder()
	GetCatalogApp(env.db).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for private app, got %d", w.Code)
	}
}

func TestGetCatalogApp_UnlistedAccessible(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
	})
	setAppVisibility(t, env, "com.foo", "unlisted")

	// Direct URL must work (200)...
	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	w := httptest.NewRecorder()
	GetCatalogApp(env.db).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for unlisted direct access, got %d", w.Code)
	}
	// ...but unlisted must not appear in the list.
	w = doCatalogGet(ListCatalogApps(env.db), "/api/v1/catalog/apps")
	if strings.Contains(w.Body.String(), "com.foo") {
		t.Errorf("unlisted app must not appear in list: %s", w.Body.String())
	}
}

// TestGetCatalogApp_AuthorEmailOmitted covers M2: the public catalog API
// must not leak the publisher's email even when the manifest supplies one.
// Dev-side handlers may keep it; the public catalog must not.
func TestGetCatalogApp_AuthorEmailOmitted(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	manifestWithEmail := strings.Replace(validManifestYAML,
		`author:
  name: "Jane"`,
		`author:
  name: "Jane"
  email: "jane@secret.example"`, 1)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": manifestWithEmail,
		"README.md":     "x",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	w := httptest.NewRecorder()
	GetCatalogApp(env.db).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "jane@secret.example") {
		t.Errorf("public catalog must not expose author email: %s", body)
	}
	if strings.Contains(body, `"email"`) {
		t.Errorf("public catalog must not include author.email field: %s", body)
	}
	// Author name should still be there (it's not PII).
	if !strings.Contains(body, `"name":"Jane"`) {
		t.Errorf("expected author.name in response, got %s", body)
	}
}

// --- ListCatalogReleases / GetCatalogRelease ---

func TestListCatalogReleases_NewestFirst(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)

	upload := func(v string) {
		mf := strings.Replace(validManifestYAML, `version: "1.0.0"`, `version: "`+v+`"`, 1)
		seedRelease(t, env, admin, "com.foo", map[string]string{"manifest.yaml": mf, "README.md": "x"})
	}
	upload("1.0.0")
	upload("2.0.0")
	upload("1.10.0")

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	w := httptest.NewRecorder()
	ListCatalogReleases(env.db).ServeHTTP(w, req)
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
	got := []string{resp.Releases[0].Version, resp.Releases[1].Version, resp.Releases[2].Version}
	want := []string{"2.0.0", "1.10.0", "1.0.0"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("position %d: got %s want %s (%+v)", i, got[i], want[i], got)
		}
	}
}

func TestGetCatalogRelease_Success(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml":         validManifestYAML,
		"README.md":             "x",
		"bin/linux-amd64/myapp": "b",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	w := httptest.NewRecorder()
	GetCatalogRelease(env.db).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"version":"1.0.0"`,
		`"platforms":["linux-amd64"]`,
		`"manifest"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body, got %s", want, body)
		}
	}
}

// --- GetCatalogDoc ---

func TestGetCatalogDoc_Success(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "# Hello World\n",
		"INSTALL.md":    "Run `make install`\n",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/docs/INSTALL.md", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	req.SetPathValue("name", "INSTALL.md")
	w := httptest.NewRecorder()
	GetCatalogDoc(env.db).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "make install") {
		t.Errorf("doc body mismatch: %s", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type=%q, want text/markdown", ct)
	}
}

func TestGetCatalogDoc_NotFound(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/docs/USAGE.md", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	req.SetPathValue("name", "USAGE.md")
	w := httptest.NewRecorder()
	GetCatalogDoc(env.db).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for absent doc, got %d", w.Code)
	}
}

// --- GetCatalogDocRendered ---

func TestGetCatalogDocRendered_Success(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "# Hello World\n\n[link](https://example.com)\n",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/docs/README.md/rendered", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	req.SetPathValue("name", "README.md")
	rec := httptest.NewRecorder()
	GetCatalogDocRendered(env.db).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type=%q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<h1") {
		t.Errorf("expected <h1, got %s", body)
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Errorf("expected link target=_blank: %s", body)
	}
}

func TestGetCatalogDocRendered_RawScriptEscaped(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     `<script>alert("xss")</script>`,
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/docs/README.md/rendered", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	req.SetPathValue("name", "README.md")
	rec := httptest.NewRecorder()
	GetCatalogDocRendered(env.db).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Errorf("raw <script> survived in rendered HTML: %s", body)
	}
	if !strings.Contains(body, "<!-- raw HTML omitted -->") {
		t.Errorf("expected raw HTML omitted marker, got %s", body)
	}
}

func TestGetCatalogDocRendered_NotFound(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/docs/USAGE.md/rendered", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	req.SetPathValue("name", "USAGE.md")
	rec := httptest.NewRecorder()
	GetCatalogDocRendered(env.db).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for absent doc, got %d", rec.Code)
	}
}

// --- GetCatalogAsset ---

func TestGetCatalogAsset_IconAndScreenshot(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)

	pkg := func() []byte {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		// manifest.yaml
		tw.WriteHeader(&tar.Header{Name: "manifest.yaml", Size: int64(len(validManifestYAML)), Typeflag: tar.TypeReg, Mode: 0o644})
		tw.Write([]byte(validManifestYAML))
		// README.md
		tw.WriteHeader(&tar.Header{Name: "README.md", Size: 5, Typeflag: tar.TypeReg, Mode: 0o644})
		tw.Write([]byte("# Hi\n"))
		// icon.png
		tw.WriteHeader(&tar.Header{Name: "icon.png", Size: 4, Typeflag: tar.TypeReg, Mode: 0o644})
		tw.Write([]byte("\x89PNG"))
		// screenshots/01.png
		tw.WriteHeader(&tar.Header{Name: "screenshots/01.png", Size: 7, Typeflag: tar.TypeReg, Mode: 0o644})
		tw.Write([]byte("screen1"))
		tw.Close()
		gw.Close()
		return buf.Bytes()
	}()

	env.seedApp(t, "com.foo", "X", admin)
	tok := env.userToken(admin, "admin@example.com", true)
	h := adminChain(env, UploadRelease(env.db, env.cfg, env.repo))
	w := uploadReq(t, h, "POST", "/api/v1/dev/apps/com.foo/releases", "com.foo", "", tok, pkg)
	if w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}

	for _, c := range []struct {
		name     string
		path     string
		wantCT   string
		wantBody string
	}{
		{"icon", "icon.png", "image/png", "\x89PNG"},
		{"screenshot_subdir", "screenshots/01.png", "image/png", "screen1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/assets/"+c.path, &bytes.Buffer{})
			req.SetPathValue("app_id", "com.foo")
			req.SetPathValue("version", "1.0.0")
			req.SetPathValue("name", c.path)
			rec := httptest.NewRecorder()
			GetCatalogAsset(env.db, env.repo).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.wantCT) {
				t.Errorf("Content-Type=%q, want prefix %q", ct, c.wantCT)
			}
			if rec.Body.String() != c.wantBody {
				t.Errorf("body=%q want %q", rec.Body.String(), c.wantBody)
			}
			if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
				t.Errorf("expected immutable cache: got %q", cc)
			}
		})
	}
}

func TestGetCatalogAsset_RejectsTraversal(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
	})

	for _, p := range []string{"../escape", "/etc/passwd", `README\md`} {
		req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/assets/"+p, &bytes.Buffer{})
		req.SetPathValue("app_id", "com.foo")
		req.SetPathValue("version", "1.0.0")
		req.SetPathValue("name", p)
		rec := httptest.NewRecorder()
		GetCatalogAsset(env.db, env.repo).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("OpenFile(%q) must 404, got %d", p, rec.Code)
		}
	}
}

// --- DownloadCatalogRelease ---

func TestDownloadCatalogRelease_FullPackage(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml":         validManifestYAML,
		"README.md":             "x",
		"bin/linux-amd64/myapp": "binary-bytes-here",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/download", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	rec := httptest.NewRecorder()
	DownloadCatalogRelease(env.db, env.repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type=%q, want application/gzip", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "com.foo-1.0.0.tar.gz") {
		t.Errorf("Content-Disposition=%q", cd)
	}
	if sha := rec.Header().Get("X-Content-SHA256"); len(sha) != 64 {
		t.Errorf("X-Content-SHA256 len=%d, want 64", len(sha))
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("full package download must be cacheable as immutable, got Cache-Control=%q", cc)
	}
}

func TestDownloadCatalogRelease_PlatformBinary(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	// validManifestYAML declares platforms.linux-amd64.path = bin/myapp,
	// so the binary must live at exactly that path.
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
		"bin/myapp":     "binary-bytes-here",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/download?platform=linux-amd64", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	rec := httptest.NewRecorder()
	DownloadCatalogRelease(env.db, env.repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "binary-bytes-here" {
		t.Errorf("body=%q want %q", rec.Body.String(), "binary-bytes-here")
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "myapp") {
		t.Errorf("Content-Disposition=%q", cd)
	}
}

func TestDownloadCatalogRelease_InvalidPlatform(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)
	seedRelease(t, env, admin, "com.foo", map[string]string{
		"manifest.yaml": validManifestYAML,
		"README.md":     "x",
	})

	req := httptest.NewRequest("GET", "/api/v1/catalog/apps/com.foo/releases/1.0.0/download?platform=freebsd-amd64", &bytes.Buffer{})
	req.SetPathValue("app_id", "com.foo")
	req.SetPathValue("version", "1.0.0")
	rec := httptest.NewRecorder()
	DownloadCatalogRelease(env.db, env.repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_platform") {
		t.Errorf("expected invalid_platform, got %s", rec.Body.String())
	}
}

// --- ListCatalogTags ---

func TestListCatalogTags_PublicOnly(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin@example.com", "p12345678", true)

	mfA := strings.Replace(validManifestYAML, `tags: ["test", "demo"]`, `tags: ["cli", "ai"]`, 1)
	mfB := strings.Replace(validManifestYAML, `tags: ["test", "demo"]`, `tags: ["cli"]`, 1)
	mfPriv := strings.Replace(validManifestYAML, `tags: ["test", "demo"]`, `tags: ["secret"]`, 1)

	seedRelease(t, env, admin, "com.a", map[string]string{"manifest.yaml": mfA, "README.md": "x"})
	seedRelease(t, env, admin, "com.b", map[string]string{"manifest.yaml": mfB, "README.md": "x"})
	seedRelease(t, env, admin, "com.priv", map[string]string{"manifest.yaml": mfPriv, "README.md": "x"})
	setAppVisibility(t, env, "com.priv", "private")

	req := httptest.NewRequest("GET", "/api/v1/catalog/tags", &bytes.Buffer{})
	rec := httptest.NewRecorder()
	ListCatalogTags(env.db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"tag":"cli"`) {
		t.Errorf("expected cli tag: %s", body)
	}
	if !strings.Contains(body, `"tag":"ai"`) {
		t.Errorf("expected ai tag: %s", body)
	}
	if strings.Contains(body, "secret") {
		t.Errorf("private app tags must not appear: %s", body)
	}
}

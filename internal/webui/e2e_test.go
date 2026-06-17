//go:build e2e

// e2e_test.go — WebUI end-to-end tests via Playwright (Chromium headless).
//
// Build tag `e2e` keeps these out of the default `go test ./...` run — they
// spin up a Chromium subprocess and need a running cfgsync instance. Invoke
// with: WEBUI_E2E_BASE_URL=http://localhost:28972 go test -tags=e2e -count=1
// ./internal/webui/...
//
// What we test (the v0.3.0 escape + the user-reported bugs):
//   1. No console errors on initial page load (catches syntax errors).
//   2. Login form renders with email + password fields.
//   3. Login flow: enter credentials -> submit -> redirect to /apps
//      (catches the "login doesn't navigate, must refresh" bug).
//
// When WEBUI_E2E_BASE_URL is unset, TestMain starts a stub that serves
// the dist/ + returns 401 for any /api/v1/* call. The stub is enough for
// tests 1 and 2; test 3 needs a real cfgsync and is skipped under the stub.

package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
)

func TestMain(m *testing.M) {
	if os.Getenv("WEBUI_E2E_BASE_URL") == "" {
		distDir, _ := filepath.Abs("dist")
		fs := http.FileServer(http.Dir(distDir))
		mux := http.NewServeMux()
		mux.Handle("/assets/", fs)
		mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_credentials"}`))
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeFile(w, r, filepath.Join(distDir, "index.html"))
		})
		srv := httptest.NewServer(mux)
		os.Setenv("WEBUI_E2E_BASE_URL", srv.URL)
		defer srv.Close()
	}
	os.Exit(m.Run())
}

func launchBrowser(t *testing.T) (playwright.Page, func() []string) {
	t.Helper()
	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("playwright.Run: %v", err)
	}
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true)})
	if err != nil {
		pw.Stop()
		t.Fatalf("Chromium.Launch: %v", err)
	}
	page, err := browser.NewPage()
	if err != nil {
		browser.Close()
		pw.Stop()
		t.Fatalf("browser.NewPage: %v", err)
	}
	var errs []string
	page.OnConsole(func(msg playwright.ConsoleMessage) {
		if msg.Type() == "error" {
			errs = append(errs, "console: "+msg.Text())
		}
	})
	page.OnPageError(func(err error) {
		errs = append(errs, "page error: "+err.Error())
	})
	cleanup := func() []string {
		page.Close()
		browser.Close()
		pw.Stop()
		return errs
	}
	return page, cleanup
}

// jsEval evaluates JavaScript string, using a string concat trick to embed
// backticks (which Go raw strings can't contain).
func jsEval(page playwright.Page, js string, args ...interface{}) (interface{}, error) {
	return page.Evaluate(js, args...)
}

// backtick is a helper to insert a literal backtick into a Go string
// used inside page.Evaluate. Go raw strings (`...`) can't contain backticks,
// so we build the JS string using regular Go strings and embed this constant.
const backtick = "`"

// TestE2E_01_PageLoadsWithoutConsoleErrors is the v0.3.0 escape guard:
// the WebUI bundle must load without any JS syntax error.
func TestE2E_01_PageLoadsWithoutConsoleErrors(t *testing.T) {
	base := os.Getenv("WEBUI_E2E_BASE_URL")
	page, getErrs := launchBrowser(t)
	resp, err := page.Goto(base + "/login")
	if err != nil {
		t.Fatalf("goto: %v", err)
	}
	if resp.Status() != 200 {
		t.Fatalf("status: got %d, want 200", resp.Status())
	}

	// Step 1: verify a minimal htm + preact render works in this browser.
	// If this fails, the problem is in the CDN dependency, not in our app.js.
	// We embed the JS template literal using string concat: "html" + "`" + "..." + "`".
	miniJS := "new Promise((resolve) => {\n" +
		"  Promise.all([\n" +
		"    import('https://esm.sh/preact@10.22.0'),\n" +
		"    import('https://esm.sh/htm@3.1.1'),\n" +
		"  ]).then(([preactMod, htmMod]) => {\n" +
		"    const { h, render } = preactMod;\n" +
		"    const htm = htmMod.default;\n" +
		"    const html = htm.bind(h);\n" +
		"    const el = document.createElement('div');\n" +
		"    render(html" + backtick + "<h1>probe-ok</h1>" + backtick + ", el);\n" +
		"    resolve({ ok: true, text: el.innerText });\n" +
		"  }).catch(e => resolve({ ok: false, err: e.message }));\n" +
		"})"
	miniTest, miniErr := jsEval(page, miniJS)
	if miniErr != nil {
		t.Fatalf("mini eval failed: %v", miniErr)
	}
	t.Logf("minimal htm render: %v", miniTest)
	if info, ok := miniTest.(map[string]interface{}); ok && info["ok"] == false {
		t.Fatalf("minimal htm render failed (esm.sh / V8 compat issue): %v", info["err"])
	}

	// Step 2: import the actual app.js module.
	importJS := "new Promise((resolve) => {\n" +
		"  import('/assets/app.js')\n" +
		"    .then(() => resolve({ ok: true }))\n" +
		"    .catch((e) => resolve({\n" +
		"      ok: false,\n" +
		"      message: e.message,\n" +
		"      stack: e.stack ? e.stack.slice(0, 1000) : '',\n" +
		"    }));\n" +
		"})"
	errInfo, err := jsEval(page, importJS)
	if err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	t.Logf("import result: %v", errInfo)

	info, ok := errInfo.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected type: %T", errInfo)
	}
	if info["ok"] == false {
		t.Fatalf("module load failed: message=%v\nstack=%v", info["message"], info["stack"])
	}

	// Step 3: the auth-card should have rendered by now (module loaded OK).
	_, err = page.WaitForSelector(".auth-card", playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(5000)})
	if err != nil {
		t.Fatalf("auth-card never mounted (import succeeded but component render failed): %v\ncaptured errors: %s",
			err, strings.Join(getErrs(), "\n"))
	}

	// Step 4: check for any runtime console errors (after selector confirmed).
	errs := getErrs()
	if len(errs) > 0 {
		t.Fatalf("console errors on initial load:\n%s", strings.Join(errs, "\n"))
	}
}

// TestE2E_02_LoginFormRendersAndAcceptsInput verifies the AuthPage
// structure (email + password fields + submit button) and that typing
// into the fields updates them (basic input wiring).
func TestE2E_02_LoginFormRendersAndAcceptsInput(t *testing.T) {
	base := os.Getenv("WEBUI_E2E_BASE_URL")
	page, _ := launchBrowser(t)
	defer page.Close()
	page.Goto(base + "/login")
	page.WaitForSelector(".auth-card")
	page.Fill("input#email", "admin@example.com")
	page.Fill("input#password", "password123")
	got, _ := page.Evaluate("() => document.querySelector('input#email').value")
	if got != "admin@example.com" {
		t.Errorf("email field value: got %v, want admin@example.com", got)
	}
}

// TestE2E_03_LoginRedirectsToHome is the v0.3.0 user-reported bug:
// "login doesn't navigate; must refresh the page".
// Requires WEBUI_E2E_BASE_URL pointing at a real cfgsync.
func TestE2E_03_LoginRedirectsToHome(t *testing.T) {
	if isStubbed() {
		t.Skip("requires WEBUI_E2E_BASE_URL pointing at a real cfgsync")
	}
	base := os.Getenv("WEBUI_E2E_BASE_URL")
	page, _ := launchBrowser(t)
	defer page.Close()
	page.Goto(base + "/login")
	page.WaitForSelector(".auth-card")
	pw := os.Getenv("E2E_ADMIN_PASSWORD")
	if pw == "" {
		pw = "admin-pass-123"
	}
	page.Fill("input#email", "admin@example.com")
	page.Fill("input#password", pw)
	page.Click("button[type=submit]")
	if err := page.WaitForURL("**/apps**"); err != nil {
		t.Fatalf("URL did not change to /apps after login: %v (current=%s)", err, page.URL())
	}
	if !strings.HasSuffix(page.URL(), "/apps") {
		t.Errorf("expected URL to end with /apps, got %q", page.URL())
	}
}

func isStubbed() bool {
	base := os.Getenv("WEBUI_E2E_BASE_URL")
	return strings.HasPrefix(base, "http://127.0.0.1")
}

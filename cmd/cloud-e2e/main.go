// Command cloud-e2e is a headless end-to-end test of the cfgsync REST API
// that mirrors every HTTP call the 1Remote client's CloudSyncService makes
// (cfgsync1: connect-string flow, health/sync probes, optimistic-lock PUT,
// version conflict, cross-app token, storage quota, history trim).
//
// Usage:
//   go run ./cmd/cloud-e2e
//   CFGSYNC_URL=http://... CFGSYNC_ADMIN_EMAIL=... CFGSYNC_ADMIN_PASSWORD=... \
//     go run ./cmd/cloud-e2e -v
//
// Exit 0 on all-pass, 1 on any failure.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	appTokenPrefix    = "1rc_"
	connectStringPref = "cfgsync1:"
	userAgent         = "1Remote-Cloud/0.1 (+https://1remote.github.io)" // matches HttpClientEx.UserAgent
)

// envOr returns the value of the env var named k, or def if unset/empty.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type client struct {
	base string
	hc   *http.Client
	tok  string
	vb   bool
}

func newClient(base string) *client {
	return &client{base: strings.TrimRight(base, "/"), hc: &http.Client{Timeout: 10 * time.Second}}
}

func (c *client) setBearer(t string) { c.tok = t }

type apiResp struct {
	status int
	body   []byte
	ct     string
}

func (c *client) do(method, path, ct string, body any, hdrs ...[2]string) (*apiResp, error) {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", ct)
	}
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	for _, h := range hdrs {
		req.Header.Set(h[0], h[1])
	}
	if c.vb {
		fmt.Fprintf(os.Stderr, "  → %s %s\n", method, path)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if c.vb {
		fmt.Fprintf(os.Stderr, "  ← %d  %s\n", resp.StatusCode, firstLine(string(b), 200))
	}
	return &apiResp{status: resp.StatusCode, body: b, ct: resp.Header.Get("Content-Type")}, nil
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (r *apiResp) mustJSON(_ *t, into any) {
	if err := json.Unmarshal(r.body, into); err != nil {
		panic(fmt.Sprintf("decode JSON (%d): %v\n  body: %s", r.status, err, firstLine(string(r.body), 200)))
	}
}

func (r *apiResp) wantCode(want int) error {
	if r.status == want {
		return nil
	}
	return fmt.Errorf("status=%d want=%d body=%s", r.status, want, firstLine(string(r.body), 200))
}

func (r *apiResp) wantCodeOne(want ...int) error {
	for _, w := range want {
		if r.status == w {
			return nil
		}
	}
	return fmt.Errorf("status=%d want one of %v body=%s", r.status, want, firstLine(string(r.body), 200))
}

func (r *apiResp) field(name string) string {
	var m map[string]any
	_ = json.Unmarshal(r.body, &m)
	if v, ok := m[name]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// ConnectString mirrors the C# ConnectString.Encode/Parse logic so the test
// can round-trip a connect string the way 1Remote's WebUI would.
type connectStringPayload struct {
	V     int    `json:"v"`
	URL   string `json:"url"`
	AppID string `json:"app_id"`
	Token string `json:"token"`
}

func encodeConnectString(urlStr, appID, token string) string {
	j, _ := json.Marshal(connectStringPayload{V: 1, URL: urlStr, AppID: appID, Token: token})
	return connectStringPref + base64.RawURLEncoding.EncodeToString(j)
}

func decodeConnectString(s string) (*connectStringPayload, error) {
	if !strings.HasPrefix(s, connectStringPref) {
		return nil, fmt.Errorf("expected prefix %q, got %q", connectStringPref, s)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, connectStringPref))
	if err != nil {
		return nil, fmt.Errorf("base64url: %w", err)
	}
	var p connectStringPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	if p.V != 1 {
		return nil, fmt.Errorf("unsupported schema v=%d", p.V)
	}
	if p.URL == "" || p.AppID == "" || p.Token == "" {
		return nil, fmt.Errorf("missing field(s): url=%q app_id=%q token=%q", p.URL, p.AppID, p.Token)
	}
	return &p, nil
}

// -----------------------------------------------------------------------------
// Tiny test harness
// -----------------------------------------------------------------------------

type tCtx struct {
	failed int
	ran    int
}

func (t *tCtx) step(name string, fn func() error) {
	t.ran++
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return fn()
	}()
	if err != nil {
		t.failed++
		fmt.Printf("  ✗ %-50s  %v\n", name, err)
		return
	}
	fmt.Printf("  ✓ %s\n", name)
}

func (t *tCtx) summary() {
	fmt.Printf("\n=== %d/%d steps passed ===\n", t.ran-t.failed, t.ran)
}

// -----------------------------------------------------------------------------
// Test
// -----------------------------------------------------------------------------

type testEnv struct {
	baseURL    string
	adminEmail string
	adminPass  string
	userEmail  string
	userPass   string
	appID      string
	appName    string
}

func main() {
	vb := flag.Bool("v", false, "verbose: log HTTP requests/responses to stderr")
	flag.Parse()

	env := testEnv{
		baseURL:    envOr("CFGSYNC_URL", "http://127.0.0.1:28972"),
		adminEmail: envOr("CFGSYNC_ADMIN_EMAIL", "admin@example.com"),
		adminPass:  envOr("CFGSYNC_ADMIN_PASSWORD", "admin-pass-123"),
	}
	ts := time.Now().UnixNano()
	env.userEmail = fmt.Sprintf("e2e-%d@example.com", ts)
	env.userPass = "e2e-pwd-" + fmt.Sprintf("%d", ts)
	// appID must be reverse-domain style: ^([a-z0-9][a-z0-9-]{1,30}\.)+[a-z0-9][a-z0-9-]{1,30}$
	env.appID = fmt.Sprintf("e2e.test.%d", ts)
	env.appName = "E2E Test App " + fmt.Sprintf("%d", ts)

	c := newClient(env.baseURL)
	c.vb = *vb
	T := &tCtx{}

	fmt.Printf("cfgsync cloud-sync E2E\n")
	fmt.Printf("  base=%s  app_id=%s  user=%s\n\n", env.baseURL, env.appID, env.userEmail)

	// ---- 1. unauthenticated health probe (mirrors ConnectStringService line 118)
	var healthBody map[string]any
	T.step("GET /health (unauthenticated)", func() error {
		r, err := c.do("GET", "/api/v1/health", "application/json", nil)
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		r.mustJSON(&t{}, &healthBody)
		if healthBody["status"] != "ok" {
			return fmt.Errorf("health.status=%v", healthBody["status"])
		}
		return nil
	})

	// ---- 2. admin login (needed to register a new app_id)
	var adminAuth struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	T.step("POST /auth/login (admin)", func() error {
		r, err := c.do("POST", "/api/v1/auth/login", "application/json", map[string]string{
			"email": env.adminEmail, "password": env.adminPass,
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		r.mustJSON(&t{}, &adminAuth)
		if adminAuth.AccessToken == "" {
			return fmt.Errorf("no access_token in response")
		}
		return nil
	})
	c.setBearer(adminAuth.AccessToken)

	// ---- 3. admin registers the test app
	T.step("POST /admin/apps (create test app)", func() error {
		r, err := c.do("POST", "/api/v1/admin/apps", "application/json", map[string]string{
			"app_id":       env.appID,
			"display_name": env.appName,
		})
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusOK)
	})

	// ---- 4. test user registration
	var userAuth struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	T.step("POST /auth/register (new user)", func() error {
		r, err := c.do("POST", "/api/v1/auth/register", "application/json", map[string]string{
			"email": env.userEmail, "password": env.userPass,
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		r.mustJSON(&t{}, &userAuth)
		return nil
	})
	c.setBearer(userAuth.AccessToken)

	// ---- 5. issue an app token (1Remote WebUI's "生成新 Token")
	var appTok struct {
		Token string `json:"token"`
		AppID string `json:"app_id"`
	}
	T.step("POST /me/apps/{app_id}/token (issue app token)", func() error {
		r, err := c.do("POST", "/api/v1/me/apps/"+env.appID+"/token", "application/json", map[string]string{
			"label": "e2e",
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		r.mustJSON(&t{}, &appTok)
		if !strings.HasPrefix(appTok.Token, appTokenPrefix) {
			return fmt.Errorf("token missing %q prefix, got %q", appTokenPrefix, appTok.Token)
		}
		return nil
	})

	// ---- 6. build a cfgsync1: connect string, round-trip it
	var connectString string
	T.step("encode cfgsync1: connect string", func() error {
		connectString = encodeConnectString(env.baseURL, env.appID, appTok.Token)
		if !strings.HasPrefix(connectString, connectStringPref) {
			return fmt.Errorf("encoded string missing prefix")
		}
		return nil
	})
	T.step("decode cfgsync1: connect string (round-trip)", func() error {
		p, err := decodeConnectString(connectString)
		if err != nil {
			return err
		}
		if p.URL != env.baseURL || p.AppID != env.appID || p.Token != appTok.Token {
			return fmt.Errorf("round-trip mismatch: %+v", p)
		}
		return nil
	})
	T.step("decode rejects missing prefix", func() error {
		_, err := decodeConnectString("not-a-connect-string")
		if err == nil {
			return fmt.Errorf("expected error, got nil")
		}
		return nil
	})
	T.step("decode rejects bad base64", func() error {
		_, err := decodeConnectString(connectStringPref + "!!!not-base64!!!")
		if err == nil {
			return fmt.Errorf("expected error, got nil")
		}
		return nil
	})

	// ---- 7. switch to app-token bearer; this is what 1Remote does in
	//         ConnectStringService.ApplyConnectStringAsync line 109-110.
	c.setBearer(appTok.Token)

	// ---- 8. sync probe (line 130) - new pair, expect version:0
	var probe1 modelConfig
	T.step("GET /apps/{app_id}/config (new pair → version=0)", func() error {
		r, err := c.do("GET", "/api/v1/apps/"+env.appID+"/config", "application/json", nil)
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		r.mustJSON(&t{}, &probe1)
		if probe1.Version != 0 {
			return fmt.Errorf("expected version=0 for new pair, got %d", probe1.Version)
		}
		if probe1.Payload != "" {
			return fmt.Errorf("expected empty payload for new pair, got %q", probe1.Payload)
		}
		return nil
	})

	// ---- 9. PUT with version=0 → expect 200 and version=1
	var put1 modelConfig
	T.step("PUT /apps/{app_id}/config (first write, version 0→1)", func() error {
		r, err := c.do("PUT", "/api/v1/apps/"+env.appID+"/config", "application/json", map[string]any{
			"version":    int64(0),
			"payload":    `{"servers":[{"id":"s1","name":"first"}]}`,
			"updated_by": "e2e-client",
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		r.mustJSON(&t{}, &put1)
		if put1.Version != 1 {
			return fmt.Errorf("expected version=1, got %d", put1.Version)
		}
		return nil
	})

	// ---- 10. GET returns the same payload
	T.step("GET /apps/{app_id}/config (round-trip after PUT)", func() error {
		r, err := c.do("GET", "/api/v1/apps/"+env.appID+"/config", "application/json", nil)
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		var got modelConfig
		r.mustJSON(&t{}, &got)
		if got.Version != 1 || got.Payload != `{"servers":[{"id":"s1","name":"first"}]}` {
			return fmt.Errorf("got %+v", got)
		}
		return nil
	})

	// ---- 11. optimistic-lock conflict: PUT with stale version=0
	T.step("PUT with stale version → 409 version_conflict", func() error {
		r, err := c.do("PUT", "/api/v1/apps/"+env.appID+"/config", "application/json", map[string]any{
			"version":    int64(0),
			"payload":    `{"stale":true}`,
			"updated_by": "e2e-stale",
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusConflict); e != nil {
			return e
		}
		if r.field("error") != "version_conflict" {
			return fmt.Errorf("expected error=version_conflict, got %q", r.field("error"))
		}
		if r.field("current_version") != "1" {
			return fmt.Errorf("expected current_version=1, got %q", r.field("current_version"))
		}
		return nil
	})

	// ---- 12. ?force=true bypasses version check (RFC v3 §4.5)
	T.step("PUT ?force=true bypasses version check", func() error {
		r, err := c.do("PUT", "/api/v1/apps/"+env.appID+"/config?force=true", "application/json", map[string]any{
			"version":    int64(0),
			"payload":    `{"force":true}`,
			"updated_by": "e2e-force",
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		var got modelConfig
		r.mustJSON(&t{}, &got)
		if got.Version != 2 {
			return fmt.Errorf("expected version=2 after force push, got %d", got.Version)
		}
		return nil
	})

	// ---- 13. invalid bearer → 401 invalid_token (ConnectStringService line 132)
	T.step("GET with no Authorization → 401", func() error {
		saved := c.tok
		c.tok = ""
		defer func() { c.tok = saved }()
		r, err := c.do("GET", "/api/v1/apps/"+env.appID+"/config", "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusUnauthorized)
	})
	T.step("GET with bogus token → 401", func() error {
		saved := c.tok
		c.tok = appTokenPrefix + "deadbeefdeadbeefdeadbeefdeadbeef"
		defer func() { c.tok = saved }()
		r, err := c.do("GET", "/api/v1/apps/"+env.appID+"/config", "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusUnauthorized)
	})

	// ---- 14. cross-app token: a token issued for app A is rejected on app B (AppTokenMW).
	//         Create a second app, then probe it with our token.
	otherAppID := env.appID + ".other"
	T.step("POST /admin/apps (second app for cross-app test)", func() error {
		// borrow admin token
		saved := c.tok
		c.tok = adminAuth.AccessToken
		defer func() { c.tok = saved }()
		r, err := c.do("POST", "/api/v1/admin/apps", "application/json", map[string]string{
			"app_id": otherAppID, "display_name": "other",
		})
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusOK)
	})
	T.step("GET /apps/{other_app_id}/config with app-A token → 403", func() error {
		c.setBearer(appTok.Token) // current token belongs to env.appID, not otherAppID
		r, err := c.do("GET", "/api/v1/apps/"+otherAppID+"/config", "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusForbidden)
	})

	// ---- 15. missing updated_by → 400
	T.step("PUT without updated_by → 400", func() error {
		c.setBearer(appTok.Token)
		r, err := c.do("PUT", "/api/v1/apps/"+env.appID+"/config", "application/json", map[string]any{
			"version": int64(2),
			"payload": `{"x":1}`,
		})
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusBadRequest)
	})

	// ---- 16. payload_too_large: 5 MB > 4 MB default cap
	T.step("PUT with > 4 MB payload → 413 payload_too_large", func() error {
		c.setBearer(appTok.Token)
		big := strings.Repeat("A", 5*1024*1024)
		r, err := c.do("PUT", "/api/v1/apps/"+env.appID+"/config", "application/json", map[string]any{
			"version":    int64(2),
			"payload":    big,
			"updated_by": "e2e",
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusRequestEntityTooLarge); e != nil {
			return e
		}
		if r.field("error") != "payload_too_large" {
			return fmt.Errorf("expected error=payload_too_large, got %q", r.field("error"))
		}
		return nil
	})

	// ---- 17. multi-PUT history growth + history trim to HISTORY_PER_APP=50 (default)
	//         Push a few more, then verify the version grows monotonically.
	for i := 0; i < 3; i++ {
		i := i
		T.step(fmt.Sprintf("PUT iteration %d (version grows)", i+1), func() error {
			c.setBearer(appTok.Token)
			// Re-read the current version so each iteration is correct regardless
			// of how many pushes happened earlier in the test.
			var cur modelConfig
			curR, err := c.do("GET", "/api/v1/apps/"+env.appID+"/config", "application/json", nil)
			if err != nil {
				return err
			}
			if e := curR.wantCode(http.StatusOK); e != nil {
				return e
			}
			_ = json.Unmarshal(curR.body, &cur)
			r, err := c.do("PUT", "/api/v1/apps/"+env.appID+"/config", "application/json", map[string]any{
				"version":    cur.Version,
				"payload":    fmt.Sprintf(`{"iter":%d}`, i),
				"updated_by": "e2e",
			})
			if err != nil {
				return err
			}
			if e := r.wantCode(http.StatusOK); e != nil {
				return e
			}
			var got modelConfig
			_ = json.Unmarshal(r.body, &got)
			if got.Version != cur.Version+1 {
				return fmt.Errorf("expected version=%d, got %d", cur.Version+1, got.Version)
			}
			return nil
		})
	}

	// ---- 18. quota endpoint reflects current usage
	T.step("GET /me/quota (after writes)", func() error {
		c.setBearer(userAuth.AccessToken)
		r, err := c.do("GET", "/api/v1/me/quota", "application/json", nil)
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		return nil
	})

	// ---- 19. list apps (user view, no admin)
	T.step("GET /apps (user can list public apps)", func() error {
		c.setBearer(userAuth.AccessToken)
		r, err := c.do("GET", "/api/v1/apps", "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusOK)
	})
	T.step("GET /apps/{app_id} (user can fetch one)", func() error {
		c.setBearer(userAuth.AccessToken)
		r, err := c.do("GET", "/api/v1/apps/"+env.appID, "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusOK)
	})

	// ---- 20. delete app data (NOTE: also revokes the app_token; tested as a unit below)
	T.step("DELETE /me/apps/{app_id}/data (wipes config + history + token)", func() error {
		c.setBearer(userAuth.AccessToken)
		r, err := c.do("DELETE", "/api/v1/me/apps/"+env.appID+"/data", "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCodeOne(http.StatusOK, http.StatusNoContent)
	})
	T.step("GET config after DeleteAppData → 401 (token was wiped along with data)", func() error {
		c.setBearer(appTok.Token)
		r, err := c.do("GET", "/api/v1/apps/"+env.appID+"/config", "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusUnauthorized)
	})

	// ---- 21. revoke app token (DELETE /me/tokens/{prefix}) and verify 401 on next probe
	//         Re-issue a token first so we have a fresh one to revoke.
	var appTok2 struct {
		Token string `json:"token"`
	}
	T.step("POST /me/apps/{app_id}/token (re-issue after wipe)", func() error {
		c.setBearer(userAuth.AccessToken)
		r, err := c.do("POST", "/api/v1/me/apps/"+env.appID+"/token", "application/json", map[string]string{
			"label": "e2e-revoked",
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		r.mustJSON(&t{}, &appTok2)
		return nil
	})
	T.step("DELETE /me/tokens/{prefix} (revoke re-issued token)", func() error {
		c.setBearer(userAuth.AccessToken)
		prefix := appTok2.Token
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		r, err := c.do("DELETE", "/api/v1/me/tokens/"+prefix, "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCodeOne(http.StatusOK, http.StatusNoContent)
	})
	T.step("GET with revoked token → 401", func() error {
		c.setBearer(appTok2.Token)
		r, err := c.do("GET", "/api/v1/apps/"+env.appID+"/config", "application/json", nil)
		if err != nil {
			return err
		}
		return r.wantCode(http.StatusUnauthorized)
	})

	// ---- 22. cleanup test apps (admin)
	T.step("DELETE /admin/apps/{app_id} (cleanup)", func() error {
		c.setBearer(adminAuth.AccessToken)
		r, err := c.do("DELETE", "/api/v1/admin/apps/"+env.appID, "application/json", nil)
		if err != nil {
			return err
		}
		if !oneOf(r.status, 200, 204, 404) {
			return fmt.Errorf("status=%d body=%s", r.status, firstLine(string(r.body), 200))
		}
		r2, _ := c.do("DELETE", "/api/v1/admin/apps/"+otherAppID, "application/json", nil)
		if !oneOf(r2.status, 200, 204, 404) {
			return fmt.Errorf("cleanup other status=%d body=%s", r2.status, firstLine(string(r2.body), 200))
		}
		return nil
	})

	// ---- 23. auth/logout works (requires refresh_token in body)
	T.step("POST /auth/logout (user)", func() error {
		c.setBearer(userAuth.AccessToken)
		r, err := c.do("POST", "/api/v1/auth/logout", "application/json", map[string]string{
			"refresh_token": userAuth.RefreshToken,
		})
		if err != nil {
			return err
		}
		return r.wantCodeOne(http.StatusOK, http.StatusNoContent)
	})

	// ---- 24. bonus: refresh token endpoint round-trip (admin)
	T.step("POST /auth/refresh (admin refresh token)", func() error {
		c.setBearer(adminAuth.AccessToken)
		r, err := c.do("POST", "/api/v1/auth/refresh", "application/json", map[string]string{
			"refresh_token": adminAuth.RefreshToken,
		})
		if err != nil {
			return err
		}
		if e := r.wantCode(http.StatusOK); e != nil {
			return e
		}
		var refreshed struct {
			AccessToken string `json:"access_token"`
		}
		r.mustJSON(&t{}, &refreshed)
		if refreshed.AccessToken == "" {
			return fmt.Errorf("no access_token in refresh response")
		}
		return nil
	})

	T.summary()
	if T.failed > 0 {
		os.Exit(1)
	}
}

type modelConfig struct {
	Version   int64  `json:"version"`
	Payload   string `json:"payload"`
	UpdatedAt int64  `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

// t is a stub used only to satisfy mustJSON's *testing.T receiver without
// pulling in the testing package at call sites. We intentionally ignore
// t.Helper / t.Fatalf's behavior; mustJSON is only called from steps where
// failure should bubble up as a returned error.
type t struct{}

func (*t) Helper()                       {}
func (*t) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}

func oneOf(v int, options ...int) bool {
	for _, o := range options {
		if v == o {
			return true
		}
	}
	return false
}

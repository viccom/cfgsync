package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_ServesIndexOnRoot(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "<!doctype html>") {
		t.Errorf("expected HTML doctype in body, got %s", w.Body.String())
	}
}

// TestHandler_AssetsMissReturn404 pins the contract that paths under /assets/
// that don't exist in the embed return 404 (not the SPA index.html fallback).
// This prevents a typo in a <script src=...> from being silently swallowed.
func TestHandler_AssetsMissReturn404(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/assets/does-not-exist.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing asset, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_SPAFallbackOnUnknownRoute(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/some/unknown/route", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (SPA fallback to index.html), got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html for SPA fallback, got %q", w.Header().Get("Content-Type"))
	}
}

func TestHandler_DoesNotPanicOnAPIPath(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handler panicked on /api/v1/health: %v", r)
		}
	}()
	h.ServeHTTP(w, req)
	_, _ = io.ReadAll(w.Body)
}

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChain_PanicPropagatesStatusToLog verifies that when an inner handler panics,
// recoverMW (running inside logMW) writes 500 through the wrappedWriter so that
// logMW records the actual status code, not the default 200.
//
// Prior to the chain-order fix, recoverMW ran OUTSIDE logMW and wrote directly
// to the underlying ResponseWriter, leaving logMW's wrappedWriter.status at 0/200
// while the client received 500 — log/client disagreed.
func TestChain_PanicPropagatesStatusToLog(t *testing.T) {
	// chain(h, logMW, recoverMW) == logMW(recoverMW(h)) — see chain() impl.
	// Outer = logMW (observes status via wrappedWriter); inner = recoverMW.
	var loggedStatus int
	capturedLogMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := &wrappedWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(ww, r)
			loggedStatus = ww.status
		})
	}

	panicH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated handler panic")
	})

	h := chain(panicH, capturedLogMW, recoverMW)

	req := httptest.NewRequest("GET", "/x", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("client saw %d, want 500", rec.Code)
	}
	if loggedStatus != http.StatusInternalServerError {
		t.Errorf("logMW recorded status %d, want 500 (chain order bug)", loggedStatus)
	}
}

// TestChain_NoPanic_PassesThrough ensures the recover+log chain doesn't
// disturb normal 200 responses.
func TestChain_NoPanic_PassesThrough(t *testing.T) {
	okH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 418 — distinctive, not 200
	})
	h := chain(okH, logMW, recoverMW)

	req := httptest.NewRequest("GET", "/x", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("got %d, want 418", rec.Code)
	}
}

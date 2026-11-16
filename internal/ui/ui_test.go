package ui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestUIEmbedAndZeroInternalIDNoise(t *testing.T) {
	raw, err := IndexHTML()
	if err != nil {
		t.Fatalf("IndexHTML failed: %v", err)
	}
	html := string(raw)

	if !strings.Contains(html, "/api/simulate") || !strings.Contains(html, "/api/probe-fcm") || !strings.Contains(html, "/api/inbox/tap") {
		t.Fatal("Control Room HTML missing required API endpoints")
	}

	// Enforce TI-3 zero internal ID noise in UI HTML.
	idRegex := regexp.MustCompile(`\b(ADR(-[0-9]+)?|AD-[0-9]+|[TDNGX][1-9]|FR-[0-9]+|NFR-[0-9]+|INV-[0-9]+)\b`)
	if match := idRegex.FindString(html); match != "" {
		t.Fatalf("TI-3 violation: internal ID %q found in index.html", match)
	}
	issueRegex := regexp.MustCompile(`(^|[^a-zA-Z0-9_&#])#[1-9][0-9]?([^a-fA-F0-9]|$)`)
	if match := issueRegex.FindString(html); match != "" {
		t.Fatalf("TI-3 violation: issue-like reference %q found in index.html", match)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Push Notification &amp; Quota-Unlocker Control Room") {
		t.Fatal("GET / did not serve expected Control Room HTML")
	}
}

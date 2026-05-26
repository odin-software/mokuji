package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRobotsTxt(t *testing.T) {
	mux := http.NewServeMux()
	mountDiscoverability(mux, "https://mokuji.org")

	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type: got %q, want text/plain", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "User-agent: *") {
		t.Errorf("body missing User-agent directive: %q", body)
	}
	if !strings.Contains(body, "Sitemap: https://mokuji.org/sitemap.xml") {
		t.Errorf("body missing sitemap reference: %q", body)
	}
}

func TestSitemapXml(t *testing.T) {
	mux := http.NewServeMux()
	mountDiscoverability(mux, "https://mokuji.org")

	req := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content-type: got %q, want application/xml", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("missing XML prolog: %q", body)
	}
	if !strings.Contains(body, "<loc>https://mokuji.org/</loc>") {
		t.Errorf("missing root URL: %q", body)
	}
}

func TestLLMsTxt(t *testing.T) {
	mux := http.NewServeMux()
	mountDiscoverability(mux, "https://mokuji.org")

	req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content-type: got %q, want text/markdown", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "# mokuji") {
		t.Errorf("body should start with title heading: %q", body[:min(40, len(body))])
	}
	if !strings.Contains(body, "AGPL") {
		t.Errorf("body missing license mention: %q", body)
	}
}

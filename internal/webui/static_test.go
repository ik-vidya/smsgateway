package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithStaticContentType(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/static/app.css", "text/css; charset=utf-8"},
		{"/static/vendor/bootstrap/bootstrap.min.css", "text/css; charset=utf-8"},
		{"/static/app.js", "application/javascript; charset=utf-8"},
		{"/static/vendor/htmx/htmx.min.js", "application/javascript; charset=utf-8"},
		{"/static/index.html", "text/html; charset=utf-8"},
		{"/static/data.json", "application/json; charset=utf-8"},
		{"/static/logo.svg", "image/svg+xml"},
		{"/static/favicon.ico", "image/x-icon"},
		{"/static/font.woff2", "font/woff2"},
		{"/static/unknown", ""},
	}
	for _, c := range cases {
		var got string
		h := withStaticContentType(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				got = w.Header().Get("Content-Type")
			}))
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got != c.want {
			t.Errorf("path=%q: Content-Type=%q, ожидалось %q",
				c.path, got, c.want)
		}
	}
}

func TestWithStaticContentTypeCaseInsensitive(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/static/APP.CSS", "text/css; charset=utf-8"},
		{"/static/MAIN.JS", "application/javascript; charset=utf-8"},
	}
	for _, c := range cases {
		var got string
		h := withStaticContentType(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				got = w.Header().Get("Content-Type")
			}))
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got != c.want {
			t.Errorf("path=%q: Content-Type=%q, ожидалось %q",
				c.path, got, c.want)
		}
	}
}

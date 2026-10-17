package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStaticHandlerServesAppCSS — реальный запрос к /static/app.css.
//
// Регрессия на баг: //go:embed static сохраняет файлы в FS с
// префиксом "static/", FileServer без fs.Sub ищет в корне
// FS и возвращает 404 (Content-Length: 19).
func TestStaticHandlerServesAppCSS(t *testing.T) {
	h := newStaticHandler()

	req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %q", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/css; charset=utf-8" {
		t.Errorf("Content-Type=%q, ожидалось text/css; charset=utf-8", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("тело пусто")
	}
	// app.css содержит "body {" — проверяем, что отдан
	// именно он, а не какой-то другой файл.
	if !strings.Contains(rec.Body.String(), "body") {
		t.Errorf("app.css без 'body' — отдан не тот файл: %q",
			rec.Body.String()[:min(80, rec.Body.Len())])
	}
}

func TestStaticHandlerServesAppJS(t *testing.T) {
	h := newStaticHandler()

	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %q", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/javascript; charset=utf-8" {
		t.Errorf("Content-Type=%q, ожидалось application/javascript",
			ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("тело пусто")
	}
}

func TestStaticHandlerServesBootstrapCSS(t *testing.T) {
	// Вложенный путь: /static/vendor/bootstrap/bootstrap.min.css.
	// Проверяет, что fs.Sub не теряет поддиректории.
	h := newStaticHandler()

	req := httptest.NewRequest(http.MethodGet,
		"/static/vendor/bootstrap/bootstrap.min.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %q", rec.Code,
			rec.Body.String()[:min(200, rec.Body.Len())])
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/css; charset=utf-8" {
		t.Errorf("Content-Type=%q", ct)
	}
	if rec.Body.Len() < 1000 {
		t.Errorf("bootstrap.min.css слишком мал: %d байт",
			rec.Body.Len())
	}
}

func TestStaticHandlerServesHtmx(t *testing.T) {
	h := newStaticHandler()

	req := httptest.NewRequest(http.MethodGet,
		"/static/vendor/htmx/htmx.min.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/javascript; charset=utf-8" {
		t.Errorf("Content-Type=%q", ct)
	}
}

// TestStaticHandler404 — несуществующий файл отдаёт 404,
// а не 200 с пустым телом (защита от слишком широкого FS).
func TestStaticHandler404(t *testing.T) {
	h := newStaticHandler()

	req := httptest.NewRequest(http.MethodGet,
		"/static/does-not-exist.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d, ожидалось 404", rec.Code)
	}
}

// TestStaticHandlerNoTraversal — попытка выйти за пределы
// static/ через ../ должна возвращать 404 (или 400), но
// не отдавать содержимое других файлов бинарника.
func TestStaticHandlerNoTraversal(t *testing.T) {
	h := newStaticHandler()

	// embed.FS запрещает .. и абсолютные пути — FileServer
	// вернёт 404. Проверяем, что не паникует и не отдаёт 200.
	for _, path := range []string{
		"/static/../server.go",
		"/static/../../../../etc/passwd",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK {
			t.Errorf("path=%q: неожиданный 200, тело: %q",
				path, rec.Body.String()[:min(100, rec.Body.Len())])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

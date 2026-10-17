// Package webui — административный веб-интерфейс smsgateway.
//
// Стек: html/template (embed) + Bootstrap 5.2 + htmx 4.0.
// Bootstrap и htmx хранятся локально в static/vendor (скачиваются
// через `make fetch`), поэтому CSP не нуждается в CDN-исключениях.
// Локализация: go-i18n/v2, ru/lv/en.
//
// В текущей версии работает только без авторизации
// (WEBUI_AUTH_MODE=none). OIDC — отдельный этап.
package webui

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/logging"
	"github.com/ik-vidya/smsgateway/internal/service"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

//go:embed templates/*.html templates/partials/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// Server — HTTP-сервер веб-UI.
type Server struct {
	cfg     *config.Config
	store   *storage.Store
	svc     *service.Service
	logs    *logging.Buffer
	logger  *slog.Logger
	version string
	envPath string

	bundle      *i18n.Bundle
	templates   map[string]*template.Template
	portWatcher *PortWatcher

	probeMu   sync.Mutex
	lastProbe time.Time

	http *http.Server
}

// New создаёт сервер.
//
// envPath — путь к .env (нужен странице /setup для сохранения
// SERIAL_PORT). Может быть пустым — тогда save откажет.
func New(cfg *config.Config, store *storage.Store, svc *service.Service,
	logs *logging.Buffer, logger *slog.Logger, version, envPath string,
) (*Server, error) {

	if logger == nil {
		logger = slog.Default()
	}
	if cfg == nil {
		return nil, errors.New("webui: cfg обязателен")
	}
	if store == nil {
		return nil, errors.New("webui: store обязателен")
	}

	if mode := cfg.WebUIAuthMode; mode != "" && mode != "none" {
		return nil, fmt.Errorf(
			"webui: WEBUI_AUTH_MODE=%q пока не поддерживается "+
				"(доступно только none)", mode)
	}

	bundle, err := buildBundle(cfg.WebUISupported, logger)
	if err != nil {
		return nil, fmt.Errorf("webui: локали: %w", err)
	}

	tmpls, err := buildTemplates()
	if err != nil {
		return nil, fmt.Errorf("webui: шаблоны: %w", err)
	}

	s := &Server{
		cfg:         cfg,
		store:       store,
		svc:         svc,
		logs:        logs,
		logger:      logger,
		version:     version,
		envPath:     envPath,
		bundle:      bundle,
		templates:   tmpls,
		portWatcher: NewPortWatcher(time.Second),
	}

	s.http = &http.Server{
		Addr:              cfg.WebUIAddr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE: /logs/stream и /setup/ports/stream
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

// Start запускает сервер и port-watcher.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.WebUIAddr)
	if err != nil {
		return fmt.Errorf("webui: listen %s: %w", s.cfg.WebUIAddr, err)
	}
	s.logger.Info("Web UI слушает", "addr", ln.Addr().String())

	stop := make(chan struct{})
	go s.portWatcher.Run(stop)
	s.http.RegisterOnShutdown(func() { close(stop) })

	err = s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown останавливает сервер.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Web UI: shutdown")
	return s.http.Shutdown(ctx)
}

// routes собирает маршруты.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /inbox", s.handleInbox)
	mux.HandleFunc("GET /outbox", s.handleOutbox)
	mux.HandleFunc("GET /compose", s.handleComposeForm)
	mux.HandleFunc("POST /compose", s.handleComposeSubmit)
	mux.HandleFunc("GET /logs", s.handleLogs)
	mux.HandleFunc("GET /logs/stream", s.handleLogsStream)
	mux.HandleFunc("GET /setup", s.handleSetup)
	mux.HandleFunc("GET /setup/ports/stream", s.handleSetupPortsStream)
	mux.HandleFunc("POST /setup/probe", s.handleSetupProbe)
	mux.HandleFunc("POST /setup/save", s.handleSetupSave)
	mux.HandleFunc("POST /setup/test", s.handleSetupTest)
	mux.HandleFunc("GET /lang/{code}", s.handleLangSet)

	mux.Handle("GET /static/", newStaticHandler())

	return withSecurityHeaders(withCSRF(mux))
}

var pageFiles = map[string]string{
	"dashboard": "dashboard",
	"compose":   "compose",
	"inbox":     "messages",
	"outbox":    "messages",
	"logs":      "logs",
	"setup":     "setup",
}

// buildTemplates собирает по отдельному *template.Template на
// каждую страницу.
func buildTemplates() (map[string]*template.Template, error) {
	out := make(map[string]*template.Template, len(pageFiles))

	funcMap := template.FuncMap{
		"T": translate,
		"or": func(a, b string) string {
			if a != "" {
				return a
			}
			return b
		},
		"truncate": truncate,
		"fmtTime":  fmtTime,
	}

	for key, file := range pageFiles {
		t := template.New(key).Funcs(funcMap)
		parsed, err := t.ParseFS(templatesFS,
			"templates/layout.html",
			"templates/partials/*.html",
			"templates/"+file+".html",
		)
		if err != nil {
			return nil, fmt.Errorf("шаблон %s (%s): %w", key, file, err)
		}
		out[key] = parsed
	}
	return out, nil
}

// render рендерит страницу в layout.
func (s *Server) render(w http.ResponseWriter, page string,
	data map[string]any) {

	tmpl, ok := s.templates[page]
	if !ok {
		s.logger.Error("webui: неизвестная страница", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		s.logger.Error("webui: execute template",
			"page", page, "err", err)
	}
}

// renderFragment рендерит только именованный фрагмент, без layout.
func (s *Server) renderFragment(w http.ResponseWriter, page, fragment string,
	data map[string]any) {

	tmpl, ok := s.templates[page]
	if !ok {
		s.logger.Error("webui: неизвестная страница", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, fragment, data); err != nil {
		s.logger.Error("webui: execute fragment",
			"page", page, "fragment", fragment, "err", err)
	}
}

// baseData собирает общие поля для всех страниц.
func (s *Server) baseData(r *http.Request, page string) map[string]any {
	lang := detectLang(r, s.cfg.WebUISupported, s.cfg.WebUIDefaultLang)
	loc := localizerFor(s.bundle, lang)

	supported := append([]string(nil), s.cfg.WebUISupported...)
	if len(supported) == 0 {
		supported = []string{"en"}
	}

	return map[string]any{
		"Lang":           lang,
		"SupportedLangs": supported,
		"Localizer":      loc,
		"ActivePage":     page,
		"Version":        s.version,
		"Title":          page,
	}
}

// truncate обрезает строку до n рун, добавляя многоточие.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// fmtTime форматирует время для таблиц.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// withStaticContentType задаёт Content-Type по расширению до
// вызова next.
//
// Причина: http.FileServer полагается на mime.TypeByExtension,
// а на сервере без /etc/mime.types (или с минимальным набором)
// он возвращает пусто. Тогда FileServer читает первые 512 байт
// и вызывает http.DetectContentType — для .css и .js это даёт
// "text/plain". С X-Content-Type-Options: nosniff браузер
// блокирует такой ресурс.
func withStaticContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext := strings.ToLower(filepath.Ext(r.URL.Path))
		var ct string
		switch ext {
		case ".css":
			ct = "text/css; charset=utf-8"
		case ".js", ".mjs":
			ct = "application/javascript; charset=utf-8"
		case ".html", ".htm":
			ct = "text/html; charset=utf-8"
		case ".json":
			ct = "application/json; charset=utf-8"
		case ".svg":
			ct = "image/svg+xml"
		case ".png":
			ct = "image/png"
		case ".jpg", ".jpeg":
			ct = "image/jpeg"
		case ".gif":
			ct = "image/gif"
		case ".ico":
			ct = "image/x-icon"
		case ".woff":
			ct = "font/woff"
		case ".woff2":
			ct = "font/woff2"
		case ".ttf":
			ct = "font/ttf"
		case ".txt":
			ct = "text/plain; charset=utf-8"
		case ".map":
			ct = "application/json; charset=utf-8"
		}
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		next.ServeHTTP(w, r)
	})
}

// newStaticHandler обслуживает /static/* из встроенного embed.FS.
//
// Важно: //go:embed static сохраняет файлы в embed.FS с
// префиксом "static/" — то есть static/app.css, а не app.css.
// FileServer ищет путь относительно корня FS, и после
// StripPrefix("/static/") от "/static/app.css" остаётся
// "app.css" — в корне FS такого файла нет, FileServer
// возвращает 404.
//
// Симптом в логах: curl -I /static/app.css →
//
//	HTTP/1.1 404 Not Found
//	Content-Length: 19   ← "404 page not found\n"
//
// Браузер при этом жалуется на MIME (text/plain для
// 404-страницы + nosniff).
//
// Решение: fs.Sub(staticFS, "static") сдвигает корень FS в
// поддерево "static/". После этого "app.css" в FileServer
// соответствует "static/app.css" в бинарнике.
func newStaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Не должно случиться: embed.FS гарантированно содержит
		// "static". Но если что-то не так — лучше 404 и лог,
		// чем паника.
		slog.Error("webui: fs.Sub(staticFS, \"static\")", "err", err)
		return http.NotFoundHandler()
	}
	return withStaticContentType(
		http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
}

// withStaticContentType задаёт Content-Type по расширению до
// вызова next.
//
// Зачем: http.FileServer полагается на mime.TypeByExtension,
// а тот на сервере без /etc/mime.types (или с минимальным
// набором) может вернуть пусто. Тогда FileServer читает
// первые 512 байт и вызывает http.DetectContentType — для
// .css и .js это даёт "text/plain". С X-Content-Type-Options:
// nosniff браузер такой ресурс блокирует:
//
//	Ресурс "…/bootstrap.min.css" был заблокирован из-за
//	несоответствия MIME-типа ("text/plain")
//	(X-Content-Type-Options: nosniff)
//
// withStaticContentType ставит Content-Type явно, до вызова
// FileServer. FileServer видит, что заголовок уже есть, и не
// перезаписывает.
//
// Путь в r.URL.Path — ещё с префиксом /static/ (обёртка стоит
// до StripPrefix), поэтому filepath.Ext работает как обычно.

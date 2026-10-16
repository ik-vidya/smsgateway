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
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/ik-vidya/smsgateway/internal/config"
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
	logger  *slog.Logger
	version string

	bundle    *i18n.Bundle
	templates map[string]*template.Template

	http *http.Server
}

// New создаёт сервер. Возвращает ошибку, если WEBUI_AUTH_MODE не
// "none" (OIDC пока не реализован) или если шаблоны/локали не
// собираются.
func New(cfg *config.Config, store *storage.Store, svc *service.Service,
	logger *slog.Logger, version string) (*Server, error) {

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
		cfg:       cfg,
		store:     store,
		svc:       svc,
		logger:    logger,
		version:   version,
		bundle:    bundle,
		templates: tmpls,
	}

	s.http = &http.Server{
		Addr:              cfg.WebUIAddr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

// Start запускает сервер и блокируется до Shutdown или ошибки.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.WebUIAddr)
	if err != nil {
		return fmt.Errorf("webui: listen %s: %w", s.cfg.WebUIAddr, err)
	}
	s.logger.Info("Web UI слушает", "addr", ln.Addr().String())

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
	mux.HandleFunc("GET /compose", s.handleComposeForm)
	mux.HandleFunc("POST /compose", s.handleComposeSubmit)
	mux.HandleFunc("GET /lang/{code}", s.handleLangSet)

	staticHandler := http.StripPrefix("/static/",
		http.FileServer(http.FS(staticFS)))
	mux.Handle("GET /static/", staticHandler)

	return withSecurityHeaders(withCSRF(mux))
}

// buildTemplates собирает по отдельному *template.Template на
// каждую страницу: layout и partials переиспользуются, "content"
// берётся из конкретной страницы.
func buildTemplates() (map[string]*template.Template, error) {
	pages := []string{"dashboard", "compose"}
	out := make(map[string]*template.Template, len(pages))

	funcMap := template.FuncMap{
		"T": translate,
		"or": func(a, b string) string {
			if a != "" {
				return a
			}
			return b
		},
	}

	for _, page := range pages {
		t := template.New(page).Funcs(funcMap)
		parsed, err := t.ParseFS(templatesFS,
			"templates/layout.html",
			"templates/partials/*.html",
			"templates/"+page+".html",
		)
		if err != nil {
			return nil, fmt.Errorf("шаблон %s: %w", page, err)
		}
		out[page] = parsed
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
		"Page":           page,
		"Version":        s.version,
		"Title":          page,
	}
}

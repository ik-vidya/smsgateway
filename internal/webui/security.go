// Package webui, файл security.go — security headers и CSRF.
package webui

import (
	"net/http"
	"net/url"
	"strings"
)

// withSecurityHeaders выставляет защитные заголовки.
//
// CSP разрешает только 'self': Bootstrap и htmx хранятся локально
// в static/vendor. 'unsafe-inline' оставлен для style-src —
// Bootstrap динамически создаёт inline-стили при показе modal
// backdrop и tooltip.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"style-src 'self' 'unsafe-inline'",
			"script-src 'self'",
			"img-src 'self' data:",
			"font-src 'self'",
			"connect-src 'self'",
			"base-uri 'self'",
			"form-action 'self'",
		}, "; "))
		next.ServeHTTP(w, r)
	})
}

// withCSRF проверяет Origin/Referer для небезопасных методов.
//
// Логика:
//   - GET/HEAD/OPTIONS пропускаются.
//   - Если заголовок Origin задан, его host должен совпадать
//     с r.Host.
//   - Если Origin пуст, но задан Referer — используем его host.
//   - Если оба пусты (curl, Go http.Client, тесты) — пропускаем:
//     это не браузерный сценарий, CSRF-атака невозможна.
//
// Вместе с SameSite=Lax на cookie "lang" этого достаточно для
// первой версии без сессий.
func withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		if !sameOrigin(r) {
			http.Error(w, "CSRF: cross-origin request rejected",
				http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	}
	ref := strings.TrimSpace(r.Header.Get("Referer"))
	if ref != "" {
		u, err := url.Parse(ref)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	}
	return true
}

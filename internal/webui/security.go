package webui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type ctxKey string

const RoleKey ctxKey = "role"

// withSecurityHeaders выставляет защитные заголовки.
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

// withBasicAuth проверяет HTTP Basic Auth и определяет роль (admin/user).
func (s *Server) withBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.WebUIAuthMode != "basic" {
			// Если авторизация выключена, все - админы.
			ctx := context.WithValue(r.Context(), RoleKey, "admin")
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		user, pass, ok := r.BasicAuth()
		if !ok {
			s.requestAuth(w)
			return
		}

		role := ""
		if user == s.cfg.WebUIAuthAdmin && pass == s.cfg.WebUIAuthAdminPass {
			role = "admin"
		} else if user == s.cfg.WebUIAuthUser && pass == s.cfg.WebUIAuthPass {
			role = "user"
		}

		if role == "" {
			s.requestAuth(w)
			return
		}

		ctx := context.WithValue(r.Context(), RoleKey, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requestAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="smsgateway"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

// requireAdmin пропускает дальше только администраторов.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(RoleKey).(string)
		if role != "admin" {
			http.Error(w, "Forbidden: admin access required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withCSRF проверяет Origin/Referer для небезопасных методов.
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

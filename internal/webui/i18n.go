// Package webui, файл i18n.go — локализация (ru/lv/en) на базе
// go-i18n/v2.
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed i18n/*.json
var i18nFS embed.FS

// buildBundle собирает Bundle со всеми поддерживаемыми локалями.
//
// Для каждой указанной локали ищется файл i18n/<lang>.json;
// отсутствие файла — не ошибка, язык просто не зарегистрируется
// (при обращении к нему go-i18n деградирует до английского).
func buildBundle(supported []string, logger *slog.Logger) (*i18n.Bundle, error) {
	if logger == nil {
		logger = slog.Default()
	}
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)

	for _, lang := range supported {
		lang = strings.TrimSpace(lang)
		if lang == "" {
			continue
		}
		path := "i18n/" + lang + ".json"
		if _, err := bundle.LoadMessageFileFS(i18nFS, path); err != nil {
			logger.Warn("webui: не удалось загрузить локаль",
				"lang", lang, "err", err)
			continue
		}
	}
	return bundle, nil
}

// localizerFor создаёт Localizer для конкретного языка.
func localizerFor(bundle *i18n.Bundle, lang string) *i18n.Localizer {
	tag := language.Make(lang)
	return i18n.NewLocalizer(bundle, tag.String())
}

// translate — вспомогательная функция для шаблонов:
//
//	{{ T . "nav.dashboard" }}
//
// Берёт Localizer из data (map[string]any) и возвращает строку
// для ключа. Если ключа нет — возвращает сам ключ (сразу видно,
// чего не хватает).
func translate(data any, messageID string) string {
	m, ok := data.(map[string]any)
	if !ok {
		return messageID
	}
	loc, ok := m["Localizer"].(*i18n.Localizer)
	if !ok || loc == nil {
		return messageID
	}
	s, err := loc.Localize(&i18n.LocalizeConfig{MessageID: messageID})
	if err != nil {
		return messageID
	}
	return s
}

// detectLang определяет язык запроса.
//
// Приоритет:
//  1. ?lang=xx         — явный выбор пользователя, не запоминается;
//  2. cookie lang=xx   — установлен при клике на переключателе;
//  3. Accept-Language  — заголовок браузера;
//  4. default          — из WEBUI_DEFAULT_LANG;
//  5. supported[0]     — на всякий случай;
//  6. "en"             — если совсем ничего.
//
// Возвращается всегда код из supported.
func detectLang(r *http.Request, supported []string, def string) string {
	if len(supported) == 0 {
		return "en"
	}

	if q := r.URL.Query().Get("lang"); q != "" && isSupportedLang(q, supported) {
		return normalizeLang(q, supported)
	}

	if c, err := r.Cookie("lang"); err == nil && c.Value != "" {
		if isSupportedLang(c.Value, supported) {
			return normalizeLang(c.Value, supported)
		}
	}

	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if i := strings.IndexByte(tag, '-'); i > 0 {
			tag = tag[:i]
		}
		if tag != "" && isSupportedLang(tag, supported) {
			return normalizeLang(tag, supported)
		}
	}

	if isSupportedLang(def, supported) {
		return normalizeLang(def, supported)
	}
	return supported[0]
}

// isSupportedLang проверяет, входит ли код в supported (без учёта
// регистра).
func isSupportedLang(code string, supported []string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return false
	}
	for _, s := range supported {
		if strings.EqualFold(s, code) {
			return true
		}
	}
	return false
}

// normalizeLang возвращает канонический код из supported (в его
// оригинальном регистре).
func normalizeLang(code string, supported []string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, s := range supported {
		if strings.EqualFold(s, code) {
			return s
		}
	}
	return code
}

// suppress unused
var _ = fmt.Sprintf

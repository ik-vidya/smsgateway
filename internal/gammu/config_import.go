// Package gammu, файл config_import.go — конвертер gammu-smsdrc
// в .env для smsgateway.
//
// Полная совместимость с gammu-smsdrc не нужна: конвертируются
// только те опции, которые имеют аналоги в нашем .env; остальные
// собираются в отчёт (Ignored) и доступны через --verbose.
//
// Маппинг:
//
//	[gammu] Connection = serial|at  → SERIAL_PORT (через Device)
//	[gammu] Device                  → SERIAL_PORT
//	[gammu] Pin / [smsd] PIN        → MODEM_PIN
//	[smsd]  Service = FILES         → проверка (иначе warning)
//	[smsd]  InboxPath               → GAMMU_INBOX / GAMMU_ROOT
//	[smsd]  OutboxPath              → GAMMU_OUTBOX / GAMMU_ROOT
//	[smsd]  SentSMSPath             → GAMMU_SENT / GAMMU_ROOT
//	[smsd]  ErrorSMSPath            → GAMMU_ERROR / GAMMU_ROOT
//	[smsd]  InboxFormat             → GAMMU_INBOX_FORMAT
//	[smsd]  MaxRetries = N          → GAMMU_OUTBOX_MAX_ATTEMPTS = N+1
//	         (в Gammu MaxRetries — число ПОВТОРОВ после первой
//	          попытки; у нас — общее число попыток)
//
// GAMMU_ROOT склеивается только с относительными подпутями. В
// рантайме это уже так (config.gammuPath проверяет filepath.IsAbs);
// в конвертере мы ищем GAMMU_ROOT как общий родитель АБСОЛЮТНЫХ
// путей. Относительные пути из исходного конфига остаются
// относительными и склеятся с найденным root. Смешение абсолютных
// и относительных допустимо.
package gammu

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GammuConfigError — фатальная ошибка конвертации.
type GammuConfigError struct {
	Section string
	Key     string
	Value   string
	Message string
}

func (e *GammuConfigError) Error() string {
	if e.Section == "" {
		return e.Message
	}
	prefix := "[" + e.Section + "]"
	if e.Key != "" {
		prefix += " " + e.Key
	}
	if e.Value != "" {
		prefix += " = " + e.Value
	}
	return prefix + ": " + e.Message
}

// ConvertResult — результат конвертации.
type ConvertResult struct {
	// Env — готовые пары ключ=значение для .env.
	Env map[string]string

	// Warnings — некритичные замечания (например, Service != FILES).
	Warnings []string

	// Ignored — опции gammu-smsdrc, не имеющие аналога.
	Ignored []ConvertedOption

	// Unparsed — строки, которые не удалось разобрать
	// (не Key = Value, неизвестные секции).
	Unparsed []string
}

// ConvertedOption — опция, попавшая в отчёт как «проигнорировано».
type ConvertedOption struct {
	Section string
	Key     string
	Value   string
}

func (o ConvertedOption) String() string {
	if o.Section == "" {
		return o.Key + " = " + o.Value
	}
	return "[" + o.Section + "] " + o.Key + " = " + o.Value
}

// ParseSMSDConfig парсит файл конфигурации gammu-smsdrc.
//
// Возвращает map «секция → map ключей». Секция "default"
// (пустое имя) содержит ключи, встретившиеся до первой [секции].
//
// Комментарии (# и ;), пустые строки и пробелы игнорируются.
// Кавычки вокруг значения опциональны.
func ParseSMSDConfig(r io.Reader) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string)
	section := ""

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := out[section]; !ok {
				out[section] = make(map[string]string)
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = unquoteINI(val)
		if _, ok := out[section]; !ok {
			out[section] = make(map[string]string)
		}
		out[section][key] = val
	}
	return out, sc.Err()
}

func unquoteINI(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// LoadSMSDConfig читает файл и парсит его.
func LoadSMSDConfig(path string) (map[string]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseSMSDConfig(f)
}

// ConvertSMSDConfig конвертирует распарсенный gammu-smsdrc в
// результат, пригодный для записи в .env.
func ConvertSMSDConfig(cfg map[string]map[string]string) (*ConvertResult, error) {
	res := &ConvertResult{Env: make(map[string]string)}

	gammuSec := cfg["gammu"]
	smsdSec := cfg["smsd"]

	knownGammu := map[string]bool{
		"connection": true,
		"device":     true,
		"pin":        true,
	}
	knownSMSD := map[string]bool{
		"service":      true,
		"inboxpath":    true,
		"outboxpath":   true,
		"sentpath":     true,
		"sentsmspath":  true,
		"errorsmspath": true,
		"errorpath":    true,
		"inboxformat":  true,
		"maxretries":   true,
		"pin":          true,
	}

	// ==================== [gammu] ====================
	if conn := gammuSec["Connection"]; conn != "" {
		lc := strings.ToLower(conn)
		switch lc {
		case "serial", "at":
			// ok
		case "irda", "bluetooth", "blue", "none":
			return nil, &GammuConfigError{
				Section: "gammu", Key: "Connection", Value: conn,
				Message: "поддерживается только serial или at",
			}
		default:
			return nil, &GammuConfigError{
				Section: "gammu", Key: "Connection", Value: conn,
				Message: "неизвестное значение; поддерживается serial или at",
			}
		}
	}
	if dev := gammuSec["Device"]; dev != "" {
		res.Env["SERIAL_PORT"] = dev
	}
	if pin := gammuSec["Pin"]; pin != "" {
		res.Env["MODEM_PIN"] = pin
	}
	for k, v := range gammuSec {
		if !knownGammu[strings.ToLower(k)] {
			res.Ignored = append(res.Ignored, ConvertedOption{
				Section: "gammu", Key: k, Value: v,
			})
		}
	}

	// ==================== [smsd] ====================
	if svc := smsdSec["Service"]; svc != "" {
		if !strings.EqualFold(svc, "FILES") {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"Service = %q; конвертер рассчитан на FILES", svc))
		}
	}

	inbox := smsdSec["InboxPath"]
	outbox := smsdSec["OutboxPath"]
	sent := smsdSec["SentSMSPath"]
	if sent == "" {
		sent = smsdSec["SentPath"]
	}
	errPath := smsdSec["ErrorSMSPath"]
	if errPath == "" {
		errPath = smsdSec["ErrorPath"]
	}

	if inbox != "" || outbox != "" || sent != "" || errPath != "" {
		root, sub, warns := splitRootAndSubdirs(inbox, outbox, sent, errPath)
		res.Warnings = append(res.Warnings, warns...)
		if root != "" {
			res.Env["GAMMU_ROOT"] = root
		}
		for _, name := range []string{"inbox", "outbox", "sent", "error"} {
			if sub[name] == "" {
				continue
			}
			key := "GAMMU_" + strings.ToUpper(name)
			res.Env[key] = sub[name]
		}
	}

	if fmtVal := smsdSec["InboxFormat"]; fmtVal != "" {
		switch strings.ToLower(fmtVal) {
		case "standard":
			res.Env["GAMMU_INBOX_FORMAT"] = "utf8"
		case "unicode":
			res.Env["GAMMU_INBOX_FORMAT"] = "unicode"
		case "utf8", "utf-8":
			res.Env["GAMMU_INBOX_FORMAT"] = "utf8"
		case "detail":
			res.Env["GAMMU_INBOX_FORMAT"] = "detail"
			res.Warnings = append(res.Warnings,
				"InboxFormat = detail: SMS Backup XML не поддерживается, "+
					"файлы будут перемещаться в error")
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"InboxFormat = %q неизвестен; оставлен default utf8", fmtVal))
		}
	}

	if mr := smsdSec["MaxRetries"]; mr != "" {
		n, err := strconv.Atoi(mr)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"MaxRetries = %q не число, пропущено", mr))
		} else if n < 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"MaxRetries = %d отрицательное, пропущено", n))
		} else {
			res.Env["GAMMU_OUTBOX_MAX_ATTEMPTS"] = strconv.Itoa(n + 1)
		}
	}

	if pin := smsdSec["PIN"]; pin != "" {
		res.Env["MODEM_PIN"] = pin
	}

	for k, v := range smsdSec {
		if !knownSMSD[strings.ToLower(k)] {
			res.Ignored = append(res.Ignored, ConvertedOption{
				Section: "smsd", Key: k, Value: v,
			})
		}
	}

	for sec, kv := range cfg {
		if sec == "" || sec == "gammu" || sec == "smsd" {
			continue
		}
		for k, v := range kv {
			res.Unparsed = append(res.Unparsed,
				fmt.Sprintf("[%s] %s = %s", sec, k, v))
		}
	}

	return res, nil
}

// splitRootAndSubdirs анализирует четыре пути из [smsd].
//
// Возвращает:
//
//   - ("", nil, nil), если ни один путь не задан;
//   - (root, sub, warns), где root — общий родитель АБСОЛЮТНЫХ
//     путей, а sub — map имя→путь для записи в GAMMU_INBOX и т.д.
//     Абсолютные пути под root превращаются в относительные;
//     относительные пути исходного конфига остаются как есть
//     (склеятся с root в рантайме); абсолютные пути вне root
//     записываются как есть.
//
// Логика поиска root:
//
//  1. Берём только абсолютные пути. Если их нет — root определить
//     нельзя, всё пишется как есть, добавляется предупреждение.
//  2. Считаем общий префикс компонент. Если его нет — root пуст,
//     все абсолютные пути пишутся как есть.
//  3. Root = "/" + filepath.Join(common...) — восстанавливаем
//     ведущий слеш, потерянный при разбиении.
func splitRootAndSubdirs(inbox, outbox, sent, errPath string) (
	string, map[string]string, []string) {

	type entry struct {
		name string
		path string
	}
	var entries []entry
	for _, e := range []entry{
		{"inbox", inbox},
		{"outbox", outbox},
		{"sent", sent},
		{"error", errPath},
	} {
		if e.path != "" {
			entries = append(entries, e)
		}
	}
	sub := make(map[string]string, len(entries))
	var warns []string
	if len(entries) == 0 {
		return "", sub, warns
	}

	var absEntries []entry
	for _, e := range entries {
		if filepath.IsAbs(e.path) {
			absEntries = append(absEntries, e)
		}
	}

	if len(absEntries) == 0 {
		// Все пути относительные — GAMMU_ROOT определить нельзя.
		for _, e := range entries {
			sub[e.name] = cleanPath(e.path)
		}
		warns = append(warns,
			"все пути относительные; GAMMU_ROOT определить нельзя — "+
				"задайте его вручную, иначе бэкенд останется выключенным")
		return "", sub, warns
	}

	var common []string
	for i, e := range absEntries {
		parts := splitPathComponents(e.path)
		if i == 0 {
			common = parts
			continue
		}
		common = longestCommonPrefix(common, parts)
	}

	if len(common) == 0 {
		// Общего префикса у абсолютных путей нет — все они
		// записываются как есть, root не выставляется.
		for _, e := range entries {
			sub[e.name] = cleanPath(e.path)
		}
		warns = append(warns,
			"абсолютные пути не имеют общего родителя; "+
				"GAMMU_ROOT не выставлен, все пути записаны как есть")
		return "", sub, warns
	}

	// Восстанавливаем ведущий слеш.
	root := "/" + filepath.Join(common...)

	for _, e := range entries {
		if !filepath.IsAbs(e.path) {
			// Относительный путь исходного конфига —
			// записываем как есть, склеится с root в рантайме.
			sub[e.name] = cleanPath(e.path)
			continue
		}
		parts := splitPathComponents(e.path)
		rel := parts[len(common):]
		if len(rel) == 0 {
			// Путь совпадает с root — не бывает в реальных
			// конфигах, но обработаем.
			continue
		}
		sub[e.name] = filepath.Join(rel...)
	}

	return root, sub, warns
}

// splitPathComponents разбивает путь на компоненты, отбрасывая
// пустые (ведущий слеш /var/spool → ["var","spool"]).
func splitPathComponents(p string) []string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return nil
	}
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

func longestCommonPrefix(a, b []string) []string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var out []string
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			break
		}
		out = append(out, a[i])
	}
	return out
}

// cleanPath убирает trailing slash и делает filepath.Clean.
func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// ErrNoGammuSection — в файле нет [gammu] и [smsd].
var ErrNoGammuSection = errors.New("в файле нет секций [gammu] или [smsd]")

// RenderEnv рендерит результат в текст .env.
func (r *ConvertResult) RenderEnv(source string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Сгенерировано из %s\n", source)
	fmt.Fprintf(&b, "# Команда: smsgateway convert-gammu-config --from %s\n",
		source)
	b.WriteString("# Проверьте значения и дополните файл до рабочего .env\n")
	b.WriteString("# (API_KEY, DB_PATH и т.д. здесь отсутствуют).\n\n")

	order := []string{
		"SERIAL_PORT", "SERIAL_BAUD", "MODEM_PIN",
		"GAMMU_ROOT",
		"GAMMU_INBOX", "GAMMU_OUTBOX", "GAMMU_SENT", "GAMMU_ERROR",
		"GAMMU_INBOX_FORMAT",
		"GAMMU_OUTBOX_MAX_ATTEMPTS",
		"GAMMU_WATCH_INTERVAL",
	}
	written := make(map[string]bool)

	for _, k := range order {
		if v, ok := r.Env[k]; ok {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
			written[k] = true
		}
	}
	var rest []string
	for k := range r.Env {
		if !written[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) > 0 {
		b.WriteString("\n")
		sortStrings(rest)
		for _, k := range rest {
			fmt.Fprintf(&b, "%s=%s\n", k, r.Env[k])
		}
	}

	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

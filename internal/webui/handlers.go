// Package webui, файл handlers.go — HTTP-обработчики страниц.
package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ik-vidya/smsgateway/internal/storage"
)

const (
	defaultMessagesLimit = 50
	maxMessagesLimit     = 500
)

// ==================== dashboard ====================

// handleDashboard — GET /.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := s.baseData(r, "dashboard")

	counts, err := s.store.CountByStatus(ctx)
	if err != nil {
		s.logger.Error("webui: CountByStatus", "err", err)
		counts = map[storage.Status]int{}
	}
	data["Counters"] = buildCounters(counts)

	data["DBPath"] = s.cfg.DBPath
	data["DBHealthy"] = true
	data["GammuEnabled"] = s.cfg.GammuEnabled()

	if s.svc != nil {
		data["Uptime"] = s.svc.Uptime().Round(time.Second).String()
		info := s.svc.ModemInfo()
		netStat := s.svc.NetworkStatus()
		csq := s.svc.LastCSQ()

		configured := s.cfg.SerialPort != "" &&
			s.cfg.SerialPort != "none"
		registered := s.svc.Registered()

		// CSQ=99 — «неизвестно/не определяется», CSQ=0 — «≤ −113 dBm».
		// В обоих случаях DBM бесполезен.
		signalKnown := csq >= 1 && csq <= 31
		var dbm int
		if signalKnown {
			dbm = -113 + 2*csq
		}

		data["Modem"] = ModemView{
			Configured:   configured,
			Registered:   registered,
			Port:         s.cfg.SerialPort,
			Profile:      s.cfg.ModemProfile,
			Manufacturer: info.Manufacturer,
			Model:        info.Model,
			Revision:     info.Revision,
			IMEI:         info.IMEI,
			Signal:       csq,
			SignalKnown:  signalKnown,
			DBM:          dbm,
			Network:      netStat,
			NetworkText:  networkStatusText(netStat),
		}
	} else {
		data["Uptime"] = "-"
		data["Modem"] = ModemView{}
	}

	s.render(w, "dashboard", data)
}

// ==================== compose ====================

// handleComposeForm — GET /compose.
func (s *Server) handleComposeForm(w http.ResponseWriter, r *http.Request) {
	data := s.baseData(r, "compose")
	data["Success"] = r.URL.Query().Get("sent") == "1"
	data["ErrorMessage"] = ""
	data["To"] = ""
	data["Text"] = ""
	s.render(w, "compose", data)
}

// handleComposeSubmit — POST /compose.
func (s *Server) handleComposeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.composeError(w, r, "compose.error.generic", "", "")
		return
	}
	to := strings.TrimSpace(r.FormValue("to"))
	text := r.FormValue("text")

	switch {
	case to == "":
		s.composeError(w, r, "compose.error.to_required", to, text)
		return
	case !strings.HasPrefix(to, "+") || len(to) < 5 || len(to) > 20:
		s.composeError(w, r, "compose.error.to_format", to, text)
		return
	case strings.TrimSpace(text) == "":
		s.composeError(w, r, "compose.error.text_required", to, text)
		return
	case len(text) > 4000:
		s.composeError(w, r, "compose.error.text_too_long", to, text)
		return
	}

	if s.svc == nil {
		s.composeError(w, r, "compose.error.generic", to, text)
		return
	}
	id, err := s.svc.EnqueueOutgoing(r.Context(), to, text)
	if err != nil {
		s.logger.Error("webui: EnqueueOutgoing", "err", err)
		s.composeError(w, r, "compose.error.generic", to, text)
		return
	}
	s.logger.Info("webui: сообщение поставлено в очередь",
		"id", id, "peer", to)
	http.Redirect(w, r, "/compose?sent=1", http.StatusSeeOther)
}

// composeError рендерит форму с сообщением об ошибке.
func (s *Server) composeError(w http.ResponseWriter, r *http.Request,
	key, to, text string) {

	data := s.baseData(r, "compose")
	data["Success"] = false
	data["ErrorMessage"] = translate(data, key)
	data["To"] = to
	data["Text"] = text
	w.WriteHeader(http.StatusUnprocessableEntity)
	s.render(w, "compose", data)
}

// ==================== messages (inbox / outbox) ====================

// handleInbox — GET /inbox.
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	s.handleMessages(w, r, storage.DirectionIn)
}

// handleOutbox — GET /outbox.
func (s *Server) handleOutbox(w http.ResponseWriter, r *http.Request) {
	s.handleMessages(w, r, storage.DirectionOut)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request,
	dir storage.Direction) {

	page := "inbox"
	basePath := "/inbox"
	if dir == storage.DirectionOut {
		page = "outbox"
		basePath = "/outbox"
	}

	// Разбор фильтров. Даже при ошибке разбора рендерим страницу —
	// просто с пустым списком и сообщением.
	filters, ferr := parseMessagesFilters(r)

	data := s.baseData(r, page)
	data["Direction"] = string(dir)
	data["BasePath"] = basePath
	data["StatusOptions"] = allStatusOptions

	if ferr != nil {
		data["ErrorMessage"] = ferr.Error()
		data["Items"] = []messageRow{}
		data["Total"] = 0
		data["Filters"] = messagesFilters{Limit: defaultMessagesLimit}
		data["PageNumber"] = 1
		data["TotalPages"] = 1
		data["HasPrev"] = false
		data["HasNext"] = false
		data["PrevURL"] = basePath
		data["NextURL"] = basePath
		s.renderMessages(w, r, page, data)
		return
	}
	data["ErrorMessage"] = ""
	data["Filters"] = filters

	items, total, err := s.store.ListMessages(r.Context(), storage.Filter{
		Direction: dir,
		Status:    filters.Status,
		Peer:      filters.Peer,
		Since:     filters.Since,
		Until:     filters.Until,
		Limit:     filters.Limit,
		Offset:    filters.Offset,
	})
	if err != nil {
		s.logger.Error("webui: ListMessages", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + filters.Limit - 1) / filters.Limit
	if totalPages < 1 {
		totalPages = 1
	}
	pageNumber := (filters.Offset / filters.Limit) + 1

	hasPrev := filters.Offset > 0
	hasNext := filters.Offset+filters.Limit < total

	prevURL := basePath
	if hasPrev {
		off := filters.Offset - filters.Limit
		if off < 0 {
			off = 0
		}
		prevURL = buildPaginationURL(basePath, filters, off)
	}
	nextURL := basePath
	if hasNext {
		nextURL = buildPaginationURL(basePath, filters,
			filters.Offset+filters.Limit)
	}

	rows := make([]messageRow, 0, len(items))
	for _, m := range items {
		rows = append(rows, messageRow{
			ID:        m.ID,
			CreatedAt: m.CreatedAt,
			Peer:      m.Peer,
			Status:    m.Status,
			StatusKey: statusLabelKey(m.Status),
			Text:      m.Text,
		})
	}

	data["Items"] = rows
	data["Total"] = total
	data["PageNumber"] = pageNumber
	data["TotalPages"] = totalPages
	data["HasPrev"] = hasPrev
	data["HasNext"] = hasNext
	data["PrevURL"] = prevURL
	data["NextURL"] = nextURL

	s.renderMessages(w, r, page, data)
}

// renderMessages выбирает layout или fragment по заголовку
// HX-Request (htmx).
func (s *Server) renderMessages(w http.ResponseWriter, r *http.Request,
	page string, data map[string]any) {

	if r.Header.Get("HX-Request") == "true" {
		s.renderFragment(w, page, "messages-table", data)
		return
	}
	s.render(w, page, data)
}

// ==================== lang ====================

// handleLangSet — GET /lang/{code}.
func (s *Server) handleLangSet(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !s.isSupportedLang(code) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "lang",
		Value:    code,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) isSupportedLang(code string) bool {
	for _, l := range s.cfg.WebUISupported {
		if l == code {
			return true
		}
	}
	return false
}

// ==================== filters ====================

// messagesFilters — разобранные параметры фильтрации.
//
// Поля SinceValue/UntilValue/StatusValue хранят исходные строки
// из query — нужны для подстановки в форму и в URL пагинации.
type messagesFilters struct {
	Peer        string
	Status      storage.Status
	StatusValue string
	Since       *time.Time
	Until       *time.Time
	SinceValue  string
	UntilValue  string
	Limit       int
	Offset      int
}

// parseMessagesFilters разбирает query. Даты — в формате
// YYYY-MM-DD (как отдаёт <input type="date">).
//
// Since — начало указанного дня (>= в SQL). Until — начало
// СЛЕДУЮЩЕГО дня, потому что в storage.Filter применяется <.
func parseMessagesFilters(r *http.Request) (messagesFilters, error) {
	q := r.URL.Query()
	f := messagesFilters{
		Peer:  strings.TrimSpace(q.Get("peer")),
		Limit: defaultMessagesLimit,
	}

	if s := strings.TrimSpace(q.Get("status")); s != "" {
		f.Status = storage.Status(s)
		f.StatusValue = s
	}

	if s := strings.TrimSpace(q.Get("since")); s != "" {
		t, err := time.ParseInLocation("2006-01-02", s, time.Local)
		if err != nil {
			return f, fmt.Errorf("since: некорректная дата %q", s)
		}
		f.Since = &t
		f.SinceValue = s
	}

	if s := strings.TrimSpace(q.Get("until")); s != "" {
		t, err := time.ParseInLocation("2006-01-02", s, time.Local)
		if err != nil {
			return f, fmt.Errorf("until: некорректная дата %q", s)
		}
		t = t.Add(24 * time.Hour)
		f.Until = &t
		f.UntilValue = s
	}

	if s := strings.TrimSpace(q.Get("limit")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return f, fmt.Errorf("limit: ожидается положительное число")
		}
		f.Limit = n
	}
	if f.Limit > maxMessagesLimit {
		f.Limit = maxMessagesLimit
	}

	if s := strings.TrimSpace(q.Get("offset")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return f, fmt.Errorf(
				"offset: ожидается неотрицательное число")
		}
		f.Offset = n
	}

	return f, nil
}

// buildPaginationURL собирает URL с сохранением активных фильтров
// и новым offset.
func buildPaginationURL(basePath string, f messagesFilters, offset int) string {
	v := url.Values{}
	if f.Peer != "" {
		v.Set("peer", f.Peer)
	}
	if f.StatusValue != "" {
		v.Set("status", f.StatusValue)
	}
	if f.SinceValue != "" {
		v.Set("since", f.SinceValue)
	}
	if f.UntilValue != "" {
		v.Set("until", f.UntilValue)
	}
	if f.Limit != defaultMessagesLimit {
		v.Set("limit", strconv.Itoa(f.Limit))
	}
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	if len(v) == 0 {
		return basePath
	}
	return basePath + "?" + v.Encode()
}

// ==================== view-типы ====================

// CounterView — карточка счётчика в dashboard.
type CounterView struct {
	Label string
	Count int
}

// ModemView — представление модема для шаблона.
//
// Configured и Registered — независимые признаки:
//   - Configured — SERIAL_PORT задан и != "none"
//     (порт-объект существует, но не обязательно открыт);
//   - Registered — CREG stat 1 или 5 (модем в сети).
//
// SignalKnown — CSQ в диапазоне 1..31. При CSQ=0 (≤−113 dBm)
// или 99 (неизвестно) в шаблоне показываем «нет сигнала»,
// DBM не вычисляется.
type ModemView struct {
	Configured   bool
	Registered   bool
	Port         string
	Profile      string
	Manufacturer string
	Model        string
	Revision     string
	IMEI         string
	Signal       int
	SignalKnown  bool
	DBM          int
	Network      int
	NetworkText  string
}

// messageRow — строка таблицы inbox/outbox.
type messageRow struct {
	ID        int64
	CreatedAt time.Time
	Peer      string
	Status    storage.Status
	StatusKey string
	Text      string
}

// statusOption — опция в select-фильтре статуса.
type statusOption struct {
	Value    string
	LabelKey string
}

var allStatusOptions = []statusOption{
	{"queued", "dashboard.status.queued"},
	{"sending", "dashboard.status.sending"},
	{"sent", "dashboard.status.sent"},
	{"delivered", "dashboard.status.delivered"},
	{"failed", "dashboard.status.failed"},
	{"sent_no_dr", "dashboard.status.sent_no_dr"},
	{"received", "dashboard.status.received"},
}

// statusLabelKey возвращает i18n-ключ для статуса.
func statusLabelKey(st storage.Status) string {
	for _, o := range allStatusOptions {
		if o.Value == string(st) {
			return o.LabelKey
		}
	}
	return "dashboard.status.other"
}

// buildCounters превращает map[Status]int в упорядоченный срез.
func buildCounters(counts map[storage.Status]int) []CounterView {
	order := []struct {
		status storage.Status
		key    string
	}{
		{storage.StatusQueued, "dashboard.status.queued"},
		{storage.StatusSending, "dashboard.status.sending"},
		{storage.StatusSent, "dashboard.status.sent"},
		{storage.StatusDelivered, "dashboard.status.delivered"},
		{storage.StatusFailed, "dashboard.status.failed"},
		{storage.StatusSentNoDR, "dashboard.status.sent_no_dr"},
		{storage.StatusReceived, "dashboard.status.received"},
	}
	out := make([]CounterView, 0, len(order))
	seen := make(map[storage.Status]bool, len(order))
	for _, o := range order {
		out = append(out, CounterView{
			Label: o.key,
			Count: counts[o.status],
		})
		seen[o.status] = true
	}
	var extra []storage.Status
	for st := range counts {
		if !seen[st] {
			extra = append(extra, st)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, st := range extra {
		out = append(out, CounterView{
			Label: string(st),
			Count: counts[st],
		})
	}
	return out
}

// networkStatusText — человекочитаемый статус регистрации.
func networkStatusText(stat int) string {
	switch stat {
	case 0:
		return "не зарегистрирован, поиск не ведётся"
	case 1:
		return "зарегистрирован (домашняя сеть)"
	case 2:
		return "идёт поиск сети"
	case 3:
		return "регистрация отклонена"
	case 4:
		return "неизвестно"
	case 5:
		return "зарегистрирован (роуминг)"
	}
	return fmt.Sprintf("статус %d", stat)
}

// statusBadge возвращает CSS-класс Bootstrap для статуса.
func statusBadge(st storage.Status) string {
	switch st {
	case storage.StatusDelivered:
		return "bg-success"
	case storage.StatusFailed:
		return "bg-danger"
	case storage.StatusSent:
		return "bg-info"
	case storage.StatusSentNoDR:
		return "bg-warning text-dark"
	case storage.StatusSending:
		return "bg-warning text-dark"
	}
	return "bg-secondary"
}

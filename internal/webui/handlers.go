// Package webui, файл handlers.go — HTTP-обработчики страниц.
package webui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ik-vidya/smsgateway/internal/storage"
)

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

	dbHealthy := true
	data["DBPath"] = s.cfg.DBPath
	data["DBHealthy"] = dbHealthy

	data["GammuEnabled"] = s.cfg.GammuEnabled()

	if s.svc != nil {
		data["Uptime"] = s.svc.Uptime().Round(time.Second).String()
		info := s.svc.ModemInfo()
		netStat := s.svc.NetworkStatus()
		csq := s.svc.LastCSQ()

		configured := s.cfg.SerialPort != "" &&
			s.cfg.SerialPort != "none"

		dbm := 0
		if csq >= 0 && csq <= 31 {
			dbm = -113 + 2*csq
		}

		data["Modem"] = ModemView{
			Configured:   configured,
			Port:         s.cfg.SerialPort,
			Profile:      s.cfg.ModemProfile,
			Manufacturer: info.Manufacturer,
			Model:        info.Model,
			Revision:     info.Revision,
			IMEI:         info.IMEI,
			Signal:       csq,
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

// ==================== view-типы ====================

// CounterView — карточка счётчика в dashboard.
type CounterView struct {
	Label string
	Count int
}

// ModemView — представление модема для шаблона.
type ModemView struct {
	Configured   bool
	Port         string
	Profile      string
	Manufacturer string
	Model        string
	Revision     string
	IMEI         string
	Signal       int
	DBM          int
	Network      int
	NetworkText  string
}

// buildCounters превращает map[Status]int в упорядоченный срез
// с локализованными подписями. Для отображения используем
// фиксированный порядок статусов.
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
	// Прочие статусы, если появятся.
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

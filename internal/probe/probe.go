// Package probe — опрос AT-модема для отчёта и генерации
// скелета профиля.
//
// Отличие от diagnostics: diagnostics собирает данные для
// мониторинга (сигнал, регистрация, SMSC, storage) и возвращает
// структурированный JSON. probe — для разработчика профиля: он
// опрашивает не только стандартные, но и опциональные команды
// (AT+SPIC, AT+CBC, AT+CSMP?, AT+CNMI?, AT+CSCS?, AT+CBAND?,
// AT+CFUN?) и по результатам рекомендует обходы.
//
// AT+COPS=? намеренно НЕ входит в Run(): он медленный (до 2-3
// минут на SIM800), и его включают отдельным флагом
// --scan-networks. Иначе probe зависал бы на любом модеме без
// сети.
package probe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ik-vidya/smsgateway/internal/profiles"
)

// Modem — минимальный интерфейс для опроса.
type Modem interface {
	Exec(ctx context.Context, cmd string) (string, error)
}

// Result — результат опроса.
type Result struct {
	Port string

	// Идентификация.
	ATI  string
	CGMI string
	CGMM string
	CGMR string
	CGSN string

	// SIM.
	CPIN string
	CCID string
	CIMI string
	SPIC string

	// Сеть.
	CSQ     string
	CREG    string
	CGREG   string
	COPS    string
	COPSRaw string // для --scan-networks, если задан

	// SMS.
	CPMS string
	CMGF string
	CSMP string
	CNMI string
	CSCS string

	// Питание и режим.
	CBC   string
	CFUN  string
	CBAND string

	// Поддержка опциональных команд. true — команда вернула
	// непустой payload; false — ERROR или таймаут.
	HasSPIC  bool
	HasCBC   bool
	HasCSMP  bool
	HasCNMI  bool
	HasCSCS  bool
	HasCBAND bool
	HasCFUN  bool

	// Ошибки отдельных команд (не фатальны).
	Errors map[string]string

	// Информация о модеме для profiles.Auto().
	Info profiles.Info

	// Определённый профиль.
	Profile profiles.Profile

	// Рекомендации по обходам (текстом).
	Recommendations []string
}

// Options — параметры Run.
type Options struct {
	// CmdTimeout — таймаут одной AT-команды. 0 — 5s.
	CmdTimeout time.Duration

	// CREGTimeout — отдельный таймаут для AT+CREG? и AT+CGREG?.
	// SIM800L в роуминге 2G может отвечать на них медленнее
	// остальных команд: 5s не хватает, 10s обычно достаточно.
	// 0 — равен CmdTimeout (совместимо с прежним поведением).
	//
	// CLI (cmd/smsgateway/cmd_probe.go) по умолчанию ставит 10s.
	CREGTimeout time.Duration

	// ScanNetworks — выполнить AT+COPS=? (медленно).
	ScanNetworks bool

	// ScanNetworksTimeout — таймаут для AT+COPS=?. 0 — 180s.
	ScanNetworksTimeout time.Duration
}

// Run опрашивает модем и возвращает Result.
//
// Ошибки отдельных команд не прерывают опрос: они пишутся в
// Errors, соответствующее поле остаётся пустым, Has* = false.
func Run(ctx context.Context, m Modem, opts Options) (*Result, error) {
	if m == nil {
		return nil, fmt.Errorf("probe: modem не задан")
	}
	if opts.CmdTimeout <= 0 {
		opts.CmdTimeout = 5 * time.Second
	}
	if opts.CREGTimeout <= 0 {
		opts.CREGTimeout = opts.CmdTimeout
	}
	if opts.ScanNetworksTimeout <= 0 {
		opts.ScanNetworksTimeout = 180 * time.Second
	}

	r := &Result{
		Errors: make(map[string]string),
	}

	// Идентификация.
	r.ATI = r.exec(ctx, m, "ATI", opts.CmdTimeout)
	r.CGMI = r.exec(ctx, m, "AT+CGMI", opts.CmdTimeout)
	r.CGMM = r.exec(ctx, m, "AT+CGMM", opts.CmdTimeout)
	r.CGMR = r.exec(ctx, m, "AT+CGMR", opts.CmdTimeout)
	r.CGSN = r.exec(ctx, m, "AT+CGSN", opts.CmdTimeout)

	r.Info = profiles.Info{
		Manufacturer: firstLine(r.CGMI),
		Model:        firstLine(r.CGMM),
		Revision:     firstLine(r.CGMR),
	}
	r.Profile = profiles.Auto(r.Info)

	// SIM.
	r.CPIN = r.exec(ctx, m, "AT+CPIN?", opts.CmdTimeout)
	r.CCID = r.exec(ctx, m, "AT+CCID", opts.CmdTimeout)
	r.CIMI = r.exec(ctx, m, "AT+CIMI", opts.CmdTimeout)
	r.SPIC = r.exec(ctx, m, "AT+SPIC", opts.CmdTimeout)
	r.HasSPIC = hasPayload(r.SPIC)

	// Сеть.
	r.CSQ = r.exec(ctx, m, "AT+CSQ", opts.CmdTimeout)
	// CREG/CGREG — отдельный (более щедрый) таймаут:
	// SIM800L в роуминге может «думать» дольше остальных команд.
	r.CREG = r.exec(ctx, m, "AT+CREG?", opts.CREGTimeout)
	r.CGREG = r.exec(ctx, m, "AT+CGREG?", opts.CREGTimeout)
	r.COPS = r.exec(ctx, m, "AT+COPS?", opts.CmdTimeout)

	if opts.ScanNetworks {
		r.COPSRaw = r.exec(ctx, m, "AT+COPS=?", opts.ScanNetworksTimeout)
	}

	// SMS.
	r.CPMS = r.exec(ctx, m, "AT+CPMS?", opts.CmdTimeout)
	r.CMGF = r.exec(ctx, m, "AT+CMGF?", opts.CmdTimeout)
	r.CSMP = r.exec(ctx, m, "AT+CSMP?", opts.CmdTimeout)
	r.HasCSMP = hasPayload(r.CSMP)
	r.CNMI = r.exec(ctx, m, "AT+CNMI?", opts.CmdTimeout)
	r.HasCNMI = hasPayload(r.CNMI)
	r.CSCS = r.exec(ctx, m, "AT+CSCS?", opts.CmdTimeout)
	r.HasCSCS = hasPayload(r.CSCS)

	// Питание и режим.
	r.CBC = r.exec(ctx, m, "AT+CBC", opts.CmdTimeout)
	r.HasCBC = hasPayload(r.CBC)
	r.CFUN = r.exec(ctx, m, "AT+CFUN?", opts.CmdTimeout)
	r.HasCFUN = hasPayload(r.CFUN)
	r.CBAND = r.exec(ctx, m, "AT+CBAND?", opts.CmdTimeout)
	r.HasCBAND = hasPayload(r.CBAND)

	r.Recommendations = buildRecommendations(r)
	return r, nil
}

// exec выполняет команду и возвращает чистый payload.
//
// При ошибке пишет её в Errors и возвращает "".
//
// Strip'ает строки "OK" и пустые строки — защита от транспорта,
// который не делает этого сам (реальный *modem.Modem.Exec уже
// стрипает; тестовые mock и сторонние реализации могут не
// стрипать).
func (r *Result) exec(ctx context.Context, m Modem, cmd string,
	timeout time.Duration) string {

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := m.Exec(cctx, cmd)
	if err != nil {
		r.Errors[cmd] = err.Error()
		return ""
	}
	return stripOK(resp)
}

// stripOK удаляет пустые строки и строки "OK" из ответа.
func stripOK(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.EqualFold(t, "OK") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// hasPayload проверяет, что ответ содержит непустой payload
// (не только OK).
func hasPayload(s string) bool {
	return strings.TrimSpace(s) != ""
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

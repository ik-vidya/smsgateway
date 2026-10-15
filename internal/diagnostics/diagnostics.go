// Package diagnostics — сбор диагностики AT-модема.
//
// Опрашивает модем набором AT-команд (ATI, CGMI, CGMM, CGMR, CGSN,
// CIMI, CCID, CPIN?, CSQ, CREG?, CGREG?, COPS?, CSCA?, CPMS?) и
// возвращает структурированный результат. Ошибка отдельной команды
// не прерывает сбор — соответствующее поле остаётся пустым, а
// исходный ответ сохраняется в Raw.
package diagnostics

import (
	"context"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Modem — минимальный интерфейс, нужный для сбора диагностики.
type Modem interface {
	Exec(ctx context.Context, cmd string) (string, error)
}

// Result — результат диагностики.
type Result struct {
	Manufacturer     string            `json:"manufacturer,omitempty"`
	Model            string            `json:"model,omitempty"`
	Revision         string            `json:"revision,omitempty"`
	IMEI             string            `json:"imei,omitempty"`
	IMSI             string            `json:"imsi,omitempty"`
	ICCID            string            `json:"iccid,omitempty"`
	SIMStatus        string            `json:"sim_status,omitempty"`
	Signal           SignalInfo        `json:"signal,omitempty"`
	Registration     RegInfo           `json:"registration,omitempty"`
	GPRSRegistration RegInfo           `json:"gprs_registration,omitempty"`
	Operator         OperatorInfo      `json:"operator,omitempty"`
	SMSC             string            `json:"smsc,omitempty"`
	Storage          StorageInfo       `json:"storage,omitempty"`
	Raw              map[string]string `json:"raw,omitempty"`
}

// SignalInfo — качество сигнала.
type SignalInfo struct {
	RSSI    int    `json:"rssi"`
	BER     int    `json:"ber"`
	DBM     int    `json:"dbm"`
	Quality string `json:"quality,omitempty"`
}

// RegInfo — статус регистрации (CREG/CGREG).
type RegInfo struct {
	Status int    `json:"status"`
	Text   string `json:"text,omitempty"`
	LAC    string `json:"lac,omitempty"`
	CID    string `json:"cid,omitempty"`
}

// OperatorInfo — информация об операторе.
type OperatorInfo struct {
	Name string `json:"name,omitempty"`
	Code string `json:"code,omitempty"`
}

// StorageInfo — состояние хранилища SMS.
type StorageInfo struct {
	Read  string `json:"read,omitempty"`
	Write string `json:"write,omitempty"`
	Used  int    `json:"used"`
	Total int    `json:"total"`
}

// Collect собирает диагностику.
func Collect(ctx context.Context, m Modem) (Result, error) {
	r := Result{Raw: make(map[string]string)}

	r.Manufacturer = trim(exec(ctx, m, "AT+CGMI", r.Raw))
	r.Model = trim(exec(ctx, m, "AT+CGMM", r.Raw))
	r.Revision = trim(exec(ctx, m, "AT+CGMR", r.Raw))
	r.IMEI = trim(exec(ctx, m, "AT+CGSN", r.Raw))
	r.IMSI = trim(exec(ctx, m, "AT+CIMI", r.Raw))
	r.ICCID = trim(exec(ctx, m, "AT+CCID", r.Raw))

	r.SIMStatus = parseSIMStatus(exec(ctx, m, "AT+CPIN?", r.Raw))
	r.Signal = parseSignal(exec(ctx, m, "AT+CSQ", r.Raw))
	r.Registration = parseCREG(exec(ctx, m, "AT+CREG?", r.Raw))
	r.GPRSRegistration = parseCREG(exec(ctx, m, "AT+CGREG?", r.Raw))
	r.Operator = parseOperator(exec(ctx, m, "AT+COPS?", r.Raw))
	r.SMSC = parseSMSC(exec(ctx, m, "AT+CSCA?", r.Raw))
	r.Storage = parseStorage(exec(ctx, m, "AT+CPMS?", r.Raw))

	return r, nil
}

// ==================== вспомогательное ====================

func exec(ctx context.Context, m Modem, cmd string, raw map[string]string) string {
	resp, err := m.Exec(ctx, cmd)
	if err != nil {
		raw[cmd] = "ERROR: " + err.Error()
		return ""
	}
	raw[cmd] = resp
	return resp
}

func trim(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// ==================== парсеры ====================

func parseSIMStatus(resp string) string {
	i := strings.Index(resp, ":")
	if i < 0 {
		return trim(resp)
	}
	return strings.TrimSpace(resp[i+1:])
}

var reCSQ = regexp.MustCompile(`\+CSQ:\s*(\d+)\s*,\s*(\d+)`)

func parseSignal(resp string) SignalInfo {
	m := reCSQ.FindStringSubmatch(resp)
	if len(m) < 3 {
		return SignalInfo{}
	}
	rssi, _ := strconv.Atoi(m[1])
	ber, _ := strconv.Atoi(m[2])
	info := SignalInfo{RSSI: rssi, BER: ber}
	if rssi == 99 {
		info.Quality = "нет данных"
		return info
	}
	info.DBM = -113 + 2*rssi
	switch {
	case rssi >= 20:
		info.Quality = "отличный"
	case rssi >= 15:
		info.Quality = "хороший"
	case rssi >= 10:
		info.Quality = "удовлетворительный"
	case rssi >= 5:
		info.Quality = "слабый"
	default:
		info.Quality = "очень слабый"
	}
	return info
}

var reCREG = regexp.MustCompile(`\+C(?:G)?REG:\s*\d+\s*,\s*(\d+)(?:\s*,\s*"([^"]*)"\s*,\s*"([^"]*)")?`)

func parseCREG(resp string) RegInfo {
	m := reCREG.FindStringSubmatch(resp)
	if len(m) < 2 {
		return RegInfo{}
	}
	stat, _ := strconv.Atoi(m[1])
	info := RegInfo{
		Status: stat,
		Text:   interpretCREG(stat),
	}
	if len(m) >= 4 {
		info.LAC = m[2]
		info.CID = m[3]
	}
	return info
}

func interpretCREG(stat int) string {
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
	return fmt.Sprintf("неизвестный статус %d", stat)
}

var reCOPS = regexp.MustCompile(`\+COPS:\s*\d+\s*,\s*(\d+)\s*,\s*"([^"]*)"`)

func parseOperator(resp string) OperatorInfo {
	m := reCOPS.FindStringSubmatch(resp)
	if len(m) < 3 {
		return OperatorInfo{}
	}
	format, _ := strconv.Atoi(m[1])
	val := m[2]
	if format == 2 {
		return OperatorInfo{Code: val}
	}
	return OperatorInfo{Name: val}
}

var reCSCA = regexp.MustCompile(`\+CSCA:\s*"([^"]*)"`)

func parseSMSC(resp string) string {
	m := reCSCA.FindStringSubmatch(resp)
	if len(m) < 2 {
		return ""
	}
	s := m[1]
	if b, err := hex.DecodeString(s); err == nil {
		out := string(b)
		if isPrintable(out) {
			return out
		}
	}
	return s
}

func isPrintable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

var reCPMS = regexp.MustCompile(
	`\+CPMS:\s*"([^"]*)",(\d+),(\d+),` +
		`"([^"]*)",(\d+),(\d+),` +
		`"([^"]*)",(\d+),(\d+)`)

func parseStorage(resp string) StorageInfo {
	m := reCPMS.FindStringSubmatch(resp)
	if len(m) < 10 {
		return StorageInfo{}
	}
	used, _ := strconv.Atoi(m[2])
	total, _ := strconv.Atoi(m[3])
	return StorageInfo{
		Read:  m[1],
		Write: m[4],
		Used:  used,
		Total: total,
	}
}

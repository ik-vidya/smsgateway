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
	"time"
)

// Таймауты диагностики.
//
// Collect шлёт ~14 AT-команд последовательно. На SIM800L
// в роуминге отдельные команды (AT+COPS?, AT+CSCA?,
// AT+CPMS?) отвечают медленно. Раньше использовался
// MODEM_CMD_TIMEOUT=5s на команду, и худший случай
// (14 × 5s) = 70s — /diagnostics висел почти минуту.
//
// Теперь: 2s на команду, 15s на весь опрос.
const (
	diagPerCmdTimeout = 2 * time.Second
	diagTotalTimeout  = 15 * time.Second
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
//
// Error заполняется, если AT+CSQ не ответил или ответ не
// парсится. В этом случае RSSI=0, DBM=0 — это НЕ «нет
// сигнала» и НЕ «0 dBm», а «состояние неизвестно».
type SignalInfo struct {
	RSSI    int    `json:"rssi"`
	BER     int    `json:"ber"`
	DBM     int    `json:"dbm"`
	Quality string `json:"quality,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RegInfo — статус регистрации (CREG/CGREG).
//
// Error заполняется, если команда не ответила (таймаут,
// модем занят) или ответ не парсится. В этом случае
// Status=0 — это НЕ «модем не зарегистрирован», а
// «состояние неизвестно». Для актуального статуса
// используйте /api/v1/status (кеш networkLoop) или
// ?raw=1 в diagnostics.
type RegInfo struct {
	Status int    `json:"status"`
	Text   string `json:"text,omitempty"`
	LAC    string `json:"lac,omitempty"`
	CID    string `json:"cid,omitempty"`
	Error  string `json:"error,omitempty"`
}

// OperatorInfo — информация об операторе.
//
// Error заполняется, если AT+COPS? не ответил.
type OperatorInfo struct {
	Name  string `json:"name,omitempty"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// StorageInfo — состояние хранилища SMS.
//
// Error заполняется, если AT+CPMS? не ответил. В этом
// случае Used=0, Total=0 — это НЕ «хранилище пустое и
// размер 0», а «состояние неизвестно».
type StorageInfo struct {
	Read  string `json:"read,omitempty"`
	Write string `json:"write,omitempty"`
	Used  int    `json:"used"`
	Total int    `json:"total"`
	Error string `json:"error,omitempty"`
}

// Collect собирает диагностику.
//
// Общий бюджет — 15 секунд, на команду — 2 секунды. Если
// команда не уложилась, поле остаётся пустым, а причина
// пишется в Raw.
//
// IMEI/IMSI/ICCID проходят через sanitizeValue: стрипается
// \r, \n, пробелы, берётся первая непустая строка. На
// SIM800L в момент таймаута UART может глюкнуть, и в IMEI
// попадает \r в середине («0\r86060»). После sanitize это
// как минимум не ломает JSON; для IMEI дополнительно
// проверяется формат (15 цифр) с warning в Raw.
func Collect(ctx context.Context, m Modem) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, diagTotalTimeout)
	defer cancel()

	r := Result{Raw: make(map[string]string)}

	execCmd := func(cmd string) string {
		cctx, ccancel := context.WithTimeout(ctx, diagPerCmdTimeout)
		defer ccancel()
		resp, err := m.Exec(cctx, cmd)
		if err != nil {
			r.Raw[cmd] = "ERROR: " + err.Error()
			return ""
		}
		r.Raw[cmd] = resp
		return resp
	}

	r.Manufacturer = sanitizeValue(execCmd("AT+CGMI"))
	r.Model = sanitizeValue(execCmd("AT+CGMM"))
	r.Revision = sanitizeValue(execCmd("AT+CGMR"))
	r.IMEI = sanitizeValue(execCmd("AT+CGSN"))
	if r.IMEI != "" && !isValidIMEI(r.IMEI) {
		r.Raw["AT+CGSN.warning"] = fmt.Sprintf(
			"invalid IMEI %q (expected 15 digits)", r.IMEI)
	}
	r.IMSI = sanitizeValue(execCmd("AT+CIMI"))
	r.ICCID = sanitizeValue(execCmd("AT+CCID"))

	r.SIMStatus = parseSIMStatus(execCmd("AT+CPIN?"))
	r.Signal = parseSignal(execCmd("AT+CSQ"))
	r.Registration = parseCREG(execCmd("AT+CREG?"))
	r.GPRSRegistration = parseCREG(execCmd("AT+CGREG?"))
	r.Operator = parseOperator(execCmd("AT+COPS?"))
	r.SMSC = parseSMSC(execCmd("AT+CSCA?"))
	r.Storage = parseStorage(execCmd("AT+CPMS?"))

	return r, nil
}

// sanitizeValue стрипает \r, \n, пробелы и берёт первую
// непустую строку.
func sanitizeValue(s string) string {
	if s == "" {
		return ""
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.ReplaceAll(line, "\r", "")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// isValidIMEI проверяет, что строка — 15 цифр.
func isValidIMEI(s string) bool {
	if len(s) != 15 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// (exec удалён: заменён на замыкание execCmd в Collect.)

func trim(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// describeEmptyResponse возвращает человекочитаемое
// описание проблемы для Raw-ответа: пустой ответ, ERROR,
// или непарсибельный текст.
func describeEmptyResponse(resp string) string {
	if resp == "" {
		return "no response (timeout or modem busy)"
	}
	if strings.HasPrefix(resp, "ERROR") ||
		strings.HasPrefix(resp, "+CME ERROR") ||
		strings.HasPrefix(resp, "+CMS ERROR") {
		return resp
	}
	t := trim(resp)
	if len(t) > 100 {
		t = t[:100] + "…"
	}
	return "unparsable: " + t
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
		return SignalInfo{Error: describeEmptyResponse(resp)}
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
		return RegInfo{Error: describeEmptyResponse(resp)}
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
		return OperatorInfo{Error: describeEmptyResponse(resp)}
	}
	format, _ := strconv.Atoi(m[1])
	val := m[2]
	if format == 2 {
		return OperatorInfo{Code: val}
	}
	return OperatorInfo{Name: val}
}

var reCSCA = regexp.MustCompile(`\+CSCA:\s*"([^"]*)"`)

// parseSMSC разбирает +CSCA? и декодирует номер SMSC.
//
// Формат ответа зависит от AT+CSCS:
//   - при CSCS="GSM" или "IRA" — ASCII-строка (номер как есть);
//   - при CSCS="UCS2" (SIM800L-профиль это устанавливает) —
//     hex UCS-2: "002B0033003700320035..." (UTF-16BE по
//     байтам, каждый символ — 4 hex-цифры с ведущими 00
//     для BMP).
//
// Алгоритм:
//  1. Если строка не hex — вернуть как есть (это уже номер).
//  2. Попробовать декодировать как ASCII-hex (для GSM/IRA
//     номеров в hex, если встретится).
//  3. Попробовать декодировать как UCS-2 (UTF-16BE) —
//     основной путь для SIM800L.
//  4. Если ни один вариант не даёт печатной строки — вернуть
//     исходный hex (пусть пользователь сам разбирается).
func parseSMSC(resp string) string {
	m := reCSCA.FindStringSubmatch(resp)
	if len(m) < 2 {
		return ""
	}
	s := m[1]
	if s == "" {
		return ""
	}

	// 1. Не hex — возвращаем как есть.
	if !looksLikeHex(s) {
		return s
	}

	b, err := hex.DecodeString(s)
	if err != nil {
		return s
	}

	// 2. ASCII: "2B333732..." → "+3725509911".
	if ascii := string(b); isPrintable(ascii) {
		return ascii
	}

	// 3. UCS-2 (UTF-16BE): "002B0033..." → "+3725509911".
	if len(b)%2 == 0 {
		if ucs2, ok := decodeUCS2BE(b); ok && isPrintable(ucs2) {
			return ucs2
		}
	}

	return s
}

// looksLikeHex — непустая строка чётной длины, все символы hex.
func looksLikeHex(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'F') ||
			(r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// decodeUCS2BE декодирует байты как UTF-16BE.
// Возвращает (строка, true) при успехе; ("", false) если
// байты не складываются в валидные руны.
func decodeUCS2BE(b []byte) (string, bool) {
	if len(b)%2 != 0 {
		return "", false
	}
	var sb strings.Builder
	for i := 0; i < len(b); i += 2 {
		u := uint16(b[i])<<8 | uint16(b[i+1])
		// Суррогатные пары (D800-DBFF + DC00-DFFF) не
		// поддерживаем — для номеров SMSC они не нужны.
		if u >= 0xD800 && u <= 0xDFFF {
			return "", false
		}
		sb.WriteRune(rune(u))
	}
	return sb.String(), true
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
		return StorageInfo{Error: describeEmptyResponse(resp)}
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

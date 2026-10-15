// Package modem, файл urc.go — типы и парсеры URC
// (Unsolicited Result Codes).
package modem

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// URC — асинхронное сообщение от модема.
type URC interface {
	Kind() string
	RawLine() string
}

// URCCMTI — "+CMTI: <mem>,<index>" — пришла новая SMS.
type URCCMTI struct {
	Mem   string
	Index int
	Raw   string
}

func (u URCCMTI) Kind() string    { return "cmti" }
func (u URCCMTI) RawLine() string { return u.Raw }

// URCCDS — "+CDS: <len>" + PDU — отчёт о доставке.
//
// Reader-горутина распознаёт "+CDS:" в одной строке, затем
// читает следующую строку как hex-PDU, декодирует её и заполняет
// Reference/TPST/Peer. Только после этого URC отправляется
// в канал urcCh.
type URCCDS struct {
	Length    int    // значение из "+CDS: <len>"
	Raw       string // строка заголовка, например "+CDS: 25"
	PDU       string // hex PDU отчёта (пусто, если не удалось получить)
	Reference int    // TP-MR (совпадает с reference отправки)
	TPST      int    // TP-ST: 0x00 = доставлено
	Peer      string // RA — номер абонента
}

func (u URCCDS) Kind() string    { return "cds" }
func (u URCCDS) RawLine() string { return u.Raw }

// URCCREG — "+CREG: ..." — изменение регистрации в сети (CS).
type URCCREG struct {
	Stat int
	LAC  string
	CID  string
	Raw  string
}

func (u URCCREG) Kind() string    { return "creg" }
func (u URCCREG) RawLine() string { return u.Raw }

// URCCGREG — "+CGREG: ..." — изменение GPRS-регистрации.
type URCCGREG struct {
	Stat int
	LAC  string
	CID  string
	Raw  string
}

func (u URCCGREG) Kind() string    { return "cgreg" }
func (u URCCGREG) RawLine() string { return u.Raw }

// URCRing — "RING" — входящий звонок.
type URCRing struct {
	Raw string
}

func (u URCRing) Kind() string    { return "ring" }
func (u URCRing) RawLine() string { return u.Raw }

// URCNoCarrier — "NO CARRIER".
type URCNoCarrier struct {
	Raw string
}

func (u URCNoCarrier) Kind() string    { return "no_carrier" }
func (u URCNoCarrier) RawLine() string { return u.Raw }

// URCCUSD — "+CUSD: ..." — USSD-ответ.
type URCCUSD struct {
	Stat int
	Text string
	Raw  string
}

func (u URCCUSD) Kind() string    { return "cusd" }
func (u URCCUSD) RawLine() string { return u.Raw }

// URCCBC — "+CBC: ..." — изменение состояния батареи.
type URCCBC struct {
	Status  int
	Percent int
	MilliV  int
	Raw     string
}

func (u URCCBC) Kind() string    { return "cbc" }
func (u URCCBC) RawLine() string { return u.Raw }

// URCRDY — "RDY" — модем перезапустился.
type URCRDY struct {
	Raw string
}

func (u URCRDY) Kind() string    { return "rdy" }
func (u URCRDY) RawLine() string { return u.Raw }

// ==================== парсеры ====================

var (
	reCMTI  = regexp.MustCompile(`^\+CMTI:\s*"([^"]*)"\s*,\s*(\d+)`)
	reCDS   = regexp.MustCompile(`^\+CDS:\s*(\d+)`)
	reCREG  = regexp.MustCompile(`^\+CREG:\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*"([^"]*)"\s*,\s*"([^"]*)")?`)
	reCGREG = regexp.MustCompile(`^\+CGREG:\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*"([^"]*)"\s*,\s*"([^"]*)")?`)
	reCUSD  = regexp.MustCompile(`^\+CUSD:\s*(\d+)\s*,\s*"([^"]*)"`)
	reCBC   = regexp.MustCompile(`^\+CBC:\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)`)
)

// parseStandardURC распознаёт стандартные URC.
//
// Возвращает (URC, true), если строка — URC.
//
// Для +CDS возвращает URCCDS с пустым PDU — reader-горутина
// должна дождаться следующей строки (hex-PDU) и дополнить его.
func parseStandardURC(line string) (URC, bool) {
	if line == "RDY" {
		return URCRDY{Raw: line}, true
	}
	if line == "RING" {
		return URCRing{Raw: line}, true
	}
	if line == "NO CARRIER" {
		return URCNoCarrier{Raw: line}, true
	}

	if m := reCMTI.FindStringSubmatch(line); m != nil {
		idx, _ := strconv.Atoi(m[2])
		return URCCMTI{Mem: m[1], Index: idx, Raw: line}, true
	}

	if m := reCDS.FindStringSubmatch(line); m != nil {
		ln, _ := strconv.Atoi(m[1])
		return URCCDS{Length: ln, Raw: line}, true
	}

	if m := reCREG.FindStringSubmatch(line); m != nil {
		stat, _ := strconv.Atoi(m[2])
		u := URCCREG{Stat: stat, Raw: line}
		if len(m) >= 5 {
			u.LAC = m[3]
			u.CID = m[4]
		}
		return u, true
	}

	if m := reCGREG.FindStringSubmatch(line); m != nil {
		stat, _ := strconv.Atoi(m[2])
		u := URCCGREG{Stat: stat, Raw: line}
		if len(m) >= 5 {
			u.LAC = m[3]
			u.CID = m[4]
		}
		return u, true
	}

	if m := reCUSD.FindStringSubmatch(line); m != nil {
		stat, _ := strconv.Atoi(m[1])
		return URCCUSD{Stat: stat, Text: m[2], Raw: line}, true
	}

	if m := reCBC.FindStringSubmatch(line); m != nil {
		st, _ := strconv.Atoi(m[1])
		pct, _ := strconv.Atoi(m[2])
		mv, _ := strconv.Atoi(m[3])
		return URCCBC{Status: st, Percent: pct, MilliV: mv, Raw: line}, true
	}

	return nil, false
}

// isHexLine проверяет, что строка — hex-последовательность
// (чётная длина, только hex-символы).
func isHexLine(s string) bool {
	if len(s) < 4 || len(s)%2 != 0 {
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

// ==================== читаемые формы ====================

// InterpretCREGStat расшифровывает код регистрации.
func InterpretCREGStat(stat int) string {
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

// InterpretTPST расшифровывает TP-ST из SMS-STATUS-REPORT.
func InterpretTPST(tpst int) string {
	switch tpst {
	case 0x00:
		return "доставлено"
	case 0x01:
		return "получено SMSC"
	case 0x02:
		return "заменено"
	case 0x20:
		return "не доставлено (нет абонента)"
	case 0x21:
		return "не доставлено (занято)"
	case 0x22:
		return "не доставлено (нет ответа)"
	case 0x40:
		return "ошибка SMSC"
	}
	return fmt.Sprintf("неизвестный статус 0x%02X", tpst)
}

// String для логов.
func (u URCCMTI) String() string {
	return fmt.Sprintf("CMTI{mem=%s, idx=%d}", u.Mem, u.Index)
}
func (u URCCDS) String() string {
	return fmt.Sprintf("CDS{ref=%d, tpst=0x%02X (%s), peer=%s}",
		u.Reference, u.TPST, InterpretTPST(u.TPST), u.Peer)
}
func (u URCCREG) String() string {
	return fmt.Sprintf("CREG{stat=%d (%s), lac=%s, cid=%s}",
		u.Stat, InterpretCREGStat(u.Stat), u.LAC, u.CID)
}
func (u URCCGREG) String() string {
	return fmt.Sprintf("CGREG{stat=%d, lac=%s, cid=%s}", u.Stat, u.LAC, u.CID)
}

// Suppress unused warnings.
var _ = strings.TrimSpace
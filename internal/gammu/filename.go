// Package gammu, файл filename.go — разбор и сборка имён файлов
// Gammu file backend.
package gammu

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Kind — направление файла.
type Kind int

const (
	KindUnknown Kind = iota
	KindIncoming
	KindOutgoing
)

// FileName — разобранное имя файла Gammu.
//
// Для входящих (IN): Time, Serial, Sender, Sequence.
// Для исходящих (OUT): Priority, Recipient, Serial, Time (если
// задан формат 3), Note (если задан формат 3).
type FileName struct {
	Raw  string
	Kind Kind
	Ext  string // без точки: "txt", "txtd", "txtdf", ...
	Time time.Time

	// Входящие.
	Sender   string
	Sequence int

	// Общие.
	Serial int

	// Исходящие.
	Priority  string // "A".."Z"
	Recipient string
	Note      string

	// Флаги из расширения.
	RequestDR bool
	Flash     bool
}

// ParseIncomingFileName разбирает имя вида
// IN<YYYYMMDD>_<HHMMSS>_<serial>_<sender>_<sequence>.<ext>.
//
// Sender может содержать «_» (alphanumeric-отправители вроде
// MY_BANK), поэтому отделяем sequence по последнему «_» в хвосте.
func ParseIncomingFileName(name string) (FileName, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	ext = strings.TrimPrefix(ext, ".")

	if !strings.HasPrefix(base, "IN") {
		return FileName{}, fmt.Errorf("не префикс IN: %q", name)
	}
	body := base[2:]

	parts := strings.SplitN(body, "_", 4)
	if len(parts) != 4 {
		return FileName{}, fmt.Errorf("некорректное имя: %q", name)
	}
	dateStr, timeStr, serialStr, tail := parts[0], parts[1], parts[2], parts[3]
	if len(dateStr) != 8 || len(timeStr) != 6 {
		return FileName{}, fmt.Errorf("некорректные дата/время в %q", name)
	}
	t, err := time.ParseInLocation("20060102150405", dateStr+timeStr, time.Local)
	if err != nil {
		return FileName{}, fmt.Errorf("разбор timestamp в %q: %w", name, err)
	}
	serial, err := atoiSafe(serialStr)
	if err != nil {
		return FileName{}, fmt.Errorf("serial в %q: %w", name, err)
	}

	last := strings.LastIndex(tail, "_")
	if last <= 0 || last == len(tail)-1 {
		return FileName{}, fmt.Errorf(
			"не найден разделитель sender/sequence в %q", name)
	}
	sender := tail[:last]
	seq, err := atoiSafe(tail[last+1:])
	if err != nil {
		return FileName{}, fmt.Errorf("sequence в %q: %w", name, err)
	}

	fn := FileName{
		Raw:      name,
		Kind:     KindIncoming,
		Ext:      ext,
		Time:     t,
		Sender:   sender,
		Serial:   serial,
		Sequence: seq,
	}
	fn.RequestDR, fn.Flash = parseExtFlags(ext)
	return fn, nil
}

// ParseOutgoingFileName разбирает имя OUT-файла.
//
// Поддерживаются три формы из документации Gammu:
//
//	OUT<recipient>.<ext>
//	OUT<priority>_<recipient>_<serial>.<ext>
//	OUT<priority><date>_<time>_<serial>_<recipient>_<note>.<ext>
//
// Определение формы — по префиксу сразу после OUT:
//
//	OUT[A-Z][0-9]{8}  → форма 3
//	OUT[A-Z]_         → форма 2
//	иначе             → форма 1
//
// Расширение должно начинаться с "txt". Хвост может содержать
// флаги "d" (delivery report) и "f" (flash) в любом порядке
// (txtd, txtf, txtdf, txtfd).
func ParseOutgoingFileName(name string) (FileName, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	ext = strings.TrimPrefix(ext, ".")

	if !strings.HasPrefix(base, "OUT") {
		return FileName{}, fmt.Errorf("не префикс OUT: %q", name)
	}
	body := base[3:]
	if body == "" {
		return FileName{}, fmt.Errorf("пустое тело имени: %q", name)
	}

	if !strings.HasPrefix(ext, "txt") {
		return FileName{}, fmt.Errorf(
			"неподдерживаемое расширение %q в %q", ext, name)
	}

	fn := FileName{Raw: name, Kind: KindOutgoing, Ext: ext}
	fn.RequestDR, fn.Flash = parseExtFlags(ext)

	switch detectOutgoingFormat(body) {
	case outgoingFull:
		return parseOutgoingFull(fn, body)
	case outgoingShort:
		return parseOutgoingShort(fn, body)
	default:
		return parseOutgoingSimple(fn, body)
	}
}

type outgoingFormat int

const (
	outgoingSimple outgoingFormat = iota
	outgoingShort
	outgoingFull
)

func detectOutgoingFormat(body string) outgoingFormat {
	if len(body) >= 10 &&
		isPriority(body[0]) &&
		digits(body[1:9]) &&
		body[9] == '_' {
		return outgoingFull
	}
	if len(body) >= 2 && isPriority(body[0]) && body[1] == '_' {
		return outgoingShort
	}
	return outgoingSimple
}

func parseOutgoingSimple(fn FileName, body string) (FileName, error) {
	if body == "" {
		return FileName{}, fmt.Errorf("пустой recipient в %q", fn.Raw)
	}
	fn.Priority = "A"
	fn.Recipient = body
	return fn, nil
}

func parseOutgoingShort(fn FileName, body string) (FileName, error) {
	// <priority>_<recipient>_<serial>
	parts := strings.Split(body, "_")
	if len(parts) < 2 {
		return FileName{}, fmt.Errorf(
			"некорректная короткая форма OUT в %q", fn.Raw)
	}
	prio := parts[0]
	if len(prio) != 1 || !isPriority(prio[0]) {
		return FileName{}, fmt.Errorf(
			"некорректный приоритет %q в %q", prio, fn.Raw)
	}
	fn.Priority = prio

	if len(parts) == 2 {
		// OUT<prio>_<recipient>.<ext> — recipient без serial.
		fn.Recipient = parts[1]
		return fn, nil
	}
	// parts >= 3: последний — serial, всё между — recipient.
	last := parts[len(parts)-1]
	serial, err := atoiSafe(last)
	if err != nil {
		// Последняя часть не число — считаем её частью recipient.
		fn.Recipient = strings.Join(parts[1:], "_")
		return fn, nil
	}
	fn.Serial = serial
	fn.Recipient = strings.Join(parts[1:len(parts)-1], "_")
	if fn.Recipient == "" {
		return FileName{}, fmt.Errorf("пустой recipient в %q", fn.Raw)
	}
	return fn, nil
}

func parseOutgoingFull(fn FileName, body string) (FileName, error) {
	// <priority><date>_<time>_<serial>_<recipient>[_<note>...]
	parts := strings.SplitN(body, "_", 5)
	if len(parts) < 4 {
		return FileName{}, fmt.Errorf(
			"некорректная полная форма OUT в %q", fn.Raw)
	}
	prioDate := parts[0]
	prio := prioDate[:1]
	if !isPriority(prio[0]) {
		return FileName{}, fmt.Errorf(
			"некорректный приоритет %q в %q", prio, fn.Raw)
	}
	fn.Priority = prio

	dateStr := prioDate[1:]
	timeStr := parts[1]
	if len(dateStr) != 8 || len(timeStr) != 6 {
		return FileName{}, fmt.Errorf("некорректные дата/время в %q", fn.Raw)
	}
	t, err := time.ParseInLocation("20060102150405",
		dateStr+timeStr, time.Local)
	if err != nil {
		return FileName{}, fmt.Errorf("разбор timestamp в %q: %w", fn.Raw, err)
	}
	fn.Time = t

	serial, err := atoiSafe(parts[2])
	if err != nil {
		return FileName{}, fmt.Errorf("serial в %q: %w", fn.Raw, err)
	}
	fn.Serial = serial

	fn.Recipient = parts[3]
	if fn.Recipient == "" {
		return FileName{}, fmt.Errorf("пустой recipient в %q", fn.Raw)
	}
	if len(parts) >= 5 {
		fn.Note = parts[4]
	}
	return fn, nil
}

// BuildIncomingFileName собирает имя входящего файла.
func BuildIncomingFileName(t time.Time, serial int, sender string, sequence int, ext string) string {
	ext = strings.TrimPrefix(ext, ".")
	return fmt.Sprintf("IN%s_%02d_%s_%02d.%s",
		t.Local().Format("20060102_150405"),
		clamp0_99(serial), sanitizeSender(sender), clamp0_99(sequence), ext)
}

// BuildOutgoingFileName собирает имя OUT-файла в короткой форме.
// Используется при retry: старое имя → новое с пониженным
// приоритетом, serial и recipient сохраняются.
func BuildOutgoingFileName(prio string, recipient string, serial int, ext string) string {
	ext = strings.TrimPrefix(ext, ".")
	return fmt.Sprintf("OUT%s_%s_%02d.%s",
		prio, sanitizeSender(recipient), clamp0_99(serial), ext)
}

// Lower возвращает приоритет на ступень ниже и ok=true.
// Из "Z" понижение невозможно — вернёт ("Z", false).
func Lower(prio string) (string, bool) {
	if prio == "" {
		return "A", true
	}
	c := prio[0]
	if c < 'A' || c > 'Z' {
		return "A", true
	}
	if c == 'Z' {
		return "Z", false
	}
	return string(c + 1), true
}

// ==================== вспомогательное ====================

func parseExtFlags(ext string) (dr, flash bool) {
	// Расширение — "txt" плюс, возможно, "d" и/или "f" в любом порядке.
	if !strings.HasPrefix(ext, "txt") {
		return false, false
	}
	tail := ext[3:]
	for _, r := range tail {
		switch r {
		case 'd', 'D':
			dr = true
		case 'f', 'F':
			flash = true
		}
	}
	return dr, flash
}

func isPriority(b byte) bool { return b >= 'A' && b <= 'Z' }

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func sanitizeSender(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
		case r == '+':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func clamp0_99(n int) int {
	switch {
	case n < 0:
		return 0
	case n > 99:
		return 99
	}
	return n
}

func atoiSafe(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("пусто")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("не число: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

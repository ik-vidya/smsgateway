// Package gammu, файл inbox.go — запись принятых SMS в формате
// Gammu file backend.
package gammu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// ErrUnsupportedInboxFormat — формат, в который мы не умеем писать.
var ErrUnsupportedInboxFormat = errors.New("неподдерживаемый формат inbox")

// InboxMessage — данные принятого SMS для записи в файл.
type InboxMessage struct {
	Peer      string
	Text      string
	SMSC      string    // у SMS-DELIVER обычно пусто
	Encoding  string    // "GSM-7" | "UCS-2" | ... влияет на Coding
	Received  time.Time // если 0 — подставляется time.Now()
	Multipart *Multipart
}

// Multipart — параметры multipart-части для UDH.
type Multipart struct {
	Reference int
	Total     int
	Sequence  int
}

// InboxWriter пишет принятые SMS в каталог inbox.
//
// Формат файла задаётся в конструкторе. Формат содержимого
// (UTF-8 или UTF-16LE) — параметр формата; поле Coding в файле
// при этом отражает исходную кодировку SMS, а не кодировку файла.
type InboxWriter struct {
	dir    string
	format string
	logger *slog.Logger
}

// NewInboxWriter создаёт writer.
//
// dir — обязателен; format — один из "utf8", "standard", "unicode".
// Пустой format равнозначен "utf8". Формат "detail" и любой
// неизвестный — ошибка.
func NewInboxWriter(dir, format string, logger *slog.Logger) (*InboxWriter, error) {
	if dir == "" {
		return nil, errors.New("каталог inbox не задан")
	}
	if logger == nil {
		logger = slog.Default()
	}

	f := normalizeInboxFormat(format)
	switch f {
	case "utf8", "unicode":
	case "detail":
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedInboxFormat, format)
	default:
		return nil, fmt.Errorf("неизвестный формат inbox %q", format)
	}

	return &InboxWriter{dir: dir, format: f, logger: logger}, nil
}

// Dir возвращает каталог, в который пишет writer.
func (w *InboxWriter) Dir() string { return w.dir }

// Format возвращает нормализованный формат ("utf8" или "unicode").
func (w *InboxWriter) Format() string { return w.format }

func normalizeInboxFormat(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "utf8", "utf-8", "standard":
		return "utf8"
	case "unicode", "utf16", "utf-16", "utf16le", "utf-16le":
		return "unicode"
	case "detail", "smsbackup":
		return "detail"
	}
	return s
}

// Write записывает одно сообщение и возвращает полный путь файла.
//
// Каталог создаётся, если его нет. Имя файла — IN<...>, serial
// подбирается как первый свободный через O_CREATE|O_EXCL. Ошибка
// отдельного файла не «залипает» на каталоге: writer остаётся
// пригодным для повторного использования.
func (w *InboxWriter) Write(msg InboxMessage) (string, error) {
	if msg.Peer == "" {
		return "", errors.New("peer обязателен")
	}
	if msg.Received.IsZero() {
		msg.Received = time.Now()
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return "", fmt.Errorf("создание каталога %q: %w", w.dir, err)
	}

	body, err := w.marshal(msg)
	if err != nil {
		return "", err
	}

	seq := 0
	if msg.Multipart != nil {
		seq = msg.Multipart.Sequence
	}

	full, err := w.writeWithFreeSerial(msg.Received, msg.Peer, seq, body)
	if err != nil {
		return "", err
	}
	w.logger.Info("gammu: inbox-файл записан",
		"path", full,
		"peer", msg.Peer,
		"format", w.format,
		"multipart", msg.Multipart != nil,
	)
	return full, nil
}

// writeWithFreeSerial пытается создать файл с serial 0, 1, 2…,
// пока не найдёт свободный. Возвращает полный путь.
func (w *InboxWriter) writeWithFreeSerial(
	t time.Time, peer string, sequence int, body []byte,
) (string, error) {
	for serial := 0; serial <= 99; serial++ {
		name := BuildIncomingFileName(t, serial, peer, sequence, "txt")
		full := filepath.Join(w.dir, name)

		f, err := os.OpenFile(full,
			os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			if _, werr := f.Write(body); werr != nil {
				_ = f.Close()
				_ = os.Remove(full)
				return "", fmt.Errorf("запись %q: %w", full, werr)
			}
			if cerr := f.Close(); cerr != nil {
				return "", fmt.Errorf("закрытие %q: %w", full, cerr)
			}
			return full, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("создание %q: %w", full, err)
		}
	}
	return "", fmt.Errorf(
		"все 100 serial-номеров для %s заняты",
		t.Local().Format("20060102_150405"))
}

// marshal собирает содержимое файла в нужной кодировке.
func (w *InboxWriter) marshal(msg InboxMessage) ([]byte, error) {
	text := buildInboxBody(msg)
	switch w.format {
	case "utf8":
		return []byte(text), nil
	case "unicode":
		return encodeUTF16LEWithBOM(text), nil
	}
	return nil, fmt.Errorf("внутренняя ошибка: формат %q", w.format)
}

// buildInboxBody собирает текстовое представление файла (UTF-8).
//
// Порядок полей совпадает с тем, что пишет gammu-smsd, чтобы
// файлы были взаимозаменяемы. Пустые поля опускаются.
func buildInboxBody(msg InboxMessage) string {
	// gammu-smsd с InboxFormat = standard/unicode пишет ТОЛЬКО
	// тело сообщения. Все метаданные (sender, timestamp,
	// sequence) — в имени файла:
	//
	//   IN<YYYYMMDD>_<HHMMSS>_<serial>_<sender>_<sequence>.<ext>
	//
	// Это делает inbox-файлы совместимыми с внешними читателями
	// (rssmspool, старые gammu-smsd).
	//
	// Полный metadata-формат (SMSC, Coding, UDH, Class) — это
	// InboxFormat = detail (SMS Backup XML), который мы не
	// поддерживаем (NewInboxWriter вернёт ошибку на detail).
	return msg.Text
}

// quote оборачивает значение в двойные кавычки.
// Внутренние кавычки заменяются на одинарные — Gammu не
// поддерживает escape-последовательности, а ломать структуру
// файла нельзя.
func quote(s string) string {
	s = strings.ReplaceAll(s, `"`, `'`)
	return `"` + s + `"`
}

func codingForEncoding(enc string) string {
	switch strings.ToUpper(strings.TrimSpace(enc)) {
	case "UCS-2", "UCS2", "UTF-16", "UTF16", "UNICODE":
		return "Unicode_No_Compression"
	}
	return "Default_No_Compression"
}

// udhHex собирает 8-битный concat-UDH: 05 00 03 <ref> <total> <seq>.
func udhHex(m *Multipart) string {
	return fmt.Sprintf("050003%02X%02X%02X",
		m.Reference&0xFF, m.Total&0xFF, m.Sequence&0xFF)
}

// encodeUTF16LEWithBOM готовит байты в UTF-16LE с BOM.
func encodeUTF16LEWithBOM(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(u16)*2)
	out = append(out, 0xFF, 0xFE) // BOM LE
	buf := make([]byte, 2)
	for _, v := range u16 {
		binary.LittleEndian.PutUint16(buf, v)
		out = append(out, buf...)
	}
	return out
}

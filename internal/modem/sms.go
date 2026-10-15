// Package modem, файл sms.go — высокоуровневые операции с SMS:
// отправка, чтение, удаление.
//
// Отправка работает в PDU-режиме (AT+CMGF=0) с использованием
// github.com/ik-vidya/sim800ctl/pkg/pdu. ForceUCS2 и RequestDR
// включаются по настройкам профиля.
package modem

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ik-vidya/sim800ctl/pkg/pdu"
)

// SendOptions — параметры отправки SMS.
type SendOptions struct {
	// RequestDR — запросить delivery report у сети.
	// На SIM800L требуется также PreSendHook профиля (AT+CSMP).
	RequestDR bool
}

// SendResult — результат отправки.
type SendResult struct {
	Reference  int64  // TP-MR из +CMGS: <ref>
	TPDULength int    // длина TPDU (без октета SMSC)
	RawPDU     string // hex PDU, отправленный в модем
}

// SendSMS собирает PDU и отправляет его через AT+CMGS.
//
// Пока не поддерживает multipart (возвращает ошибку, если текст
// не помещается в одну часть). Multipart появится позже, когда
// сервисный слой научится связывать части по UDH reference.
func (m *Modem) SendSMS(ctx context.Context, peer, text string, opts SendOptions) (SendResult, error) {
	// 1. PDU-режим.
	if _, err := m.Exec(ctx, "AT+CMGF=0"); err != nil {
		return SendResult{}, fmt.Errorf("включение PDU-режима: %w", err)
	}

	// 2. PreSendHook профиля (для SIM800L — AT+CSMP=49,167,0,0).
	if p := m.Profile(); p != nil {
		if err := p.PreSendHook(ctx, m); err != nil {
			m.logger.Warn("modem: PreSendHook вернул ошибку",
				"profile", p.Name(), "err", err)
		}
	}

	// 3. Собрать PDU с учётом обходов профиля.
	buildOpts := pdu.BuildOptions{
		ForceUCS2: false,
		RequestDR: opts.RequestDR,
	}
	if p := m.Profile(); p != nil {
		buildOpts.ForceUCS2 = p.Workarounds().ForceUCS2ForAllSMS
	}

	parts, err := pdu.BuildWith(peer, text, buildOpts)
	if err != nil {
		return SendResult{}, fmt.Errorf("сборка PDU: %w", err)
	}
	if len(parts) == 0 {
		return SendResult{}, fmt.Errorf("сборка PDU: пустой результат")
	}
	if len(parts) > 1 {
		return SendResult{}, fmt.Errorf(
			"multipart не поддержан на этом уровне: частей %d", len(parts))
	}

	part := parts[0]

	// 4. Отправить через ExecPrompt.
	cmd := fmt.Sprintf("AT+CMGS=%d", part.TpduLength)
	payload := []byte(part.Hex + "\x1a")

	resp, err := m.transport.ExecPrompt(ctx, cmd, payload)
	if err != nil {
		return SendResult{
			TPDULength: part.TpduLength,
			RawPDU:     part.Hex,
		}, fmt.Errorf("AT+CMGS: %w", err)
	}

	// 5. Разобрать +CMGS: <ref>.
	ref, err := parseCMGSReference(resp.Payload())
	if err != nil {
		return SendResult{
			TPDULength: part.TpduLength,
			RawPDU:     part.Hex,
		}, err
	}

	m.logger.Info("modem: SMS отправлено",
		"peer", peer,
		"reference", ref,
		"tpdu_len", part.TpduLength,
	)

	return SendResult{
		Reference:  ref,
		TPDULength: part.TpduLength,
		RawPDU:     part.Hex,
	}, nil
}

// parseCMGSReference ищет "+CMGS: <n>" в payload.
func parseCMGSReference(lines []string) (int64, error) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+CMGS:") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "+CMGS:"))
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("разбор +CMGS %q: %w", body, err)
		}
		return n, nil
	}
	return 0, fmt.Errorf("+CMGS не найден в ответе модема")
}

// ==================== Read ====================

// SMSMessage — принятое SMS.
type SMSMessage struct {
	Index     int
	Status    string
	Peer      string
	Text      string
	Timestamp time.Time
	RawPDU    string
	Encoding  string
	Multipart *MultipartInfo
}

// MultipartInfo — параметры multipart-части.
type MultipartInfo struct {
	Reference int
	Total     int
	Sequence  int
}

// ReadFilter — фильтр чтения SMS.
type ReadFilter struct {
	// UnreadOnly — читать только непрочитанные.
	UnreadOnly bool
}

// ReadSMS читает SMS из памяти модема в PDU-режиме.
func (m *Modem) ReadSMS(ctx context.Context, filter ReadFilter) ([]SMSMessage, error) {
	if _, err := m.Exec(ctx, "AT+CMGF=0"); err != nil {
		return nil, fmt.Errorf("включение PDU-режима: %w", err)
	}

	// stat: 0 = unread, 4 = all.
	stat := "4"
	if filter.UnreadOnly {
		stat = "0"
	}
	cmd := fmt.Sprintf("AT+CMGL=%s", stat)

	resp, err := m.transport.Exec(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("AT+CMGL: %w", err)
	}
	return parseCMGLPDU(resp.Payload())
}

// parseCMGLPDU разбирает payload ответа AT+CMGL=4.
func parseCMGLPDU(lines []string) ([]SMSMessage, error) {
	var out []SMSMessage
	var current *SMSMessage

	flush := func() {
		if current == nil || current.RawPDU == "" {
			current = nil
			return
		}
		d, err := pdu.Decode(current.RawPDU)
		if err != nil {
			current = nil
			return
		}
		current.Peer = d.Peer
		current.Text = d.Text
		current.Timestamp = d.Timestamp
		current.Encoding = d.Encoding
		if d.Multipart != nil {
			current.Multipart = &MultipartInfo{
				Reference: d.Multipart.Reference,
				Total:     d.Multipart.Total,
				Sequence:  d.Multipart.Sequence,
			}
		}
		out = append(out, *current)
		current = nil
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "+CMGL:") {
			flush()
			current = &SMSMessage{}
			parseCMGLHeader(line, current)
			continue
		}
		if current != nil && current.RawPDU == "" && isHexLine(line) {
			current.RawPDU = line
		}
	}
	flush()
	return out, nil
}

// parseCMGLHeader разбирает "+CMGL: <idx>,<stat>,[<alpha>],<len>".
func parseCMGLHeader(line string, msg *SMSMessage) {
	body := strings.TrimSpace(strings.TrimPrefix(line, "+CMGL:"))
	parts := strings.Split(body, ",")
	if len(parts) < 1 {
		return
	}
	msg.Index, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	if len(parts) >= 2 {
		stat, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		msg.Status = pduStatText(stat)
	}
}

// pduStatText расшифровывает числовой код статуса в текстовый
// (соответствие из 3GPP TS 27.005 §4.1).
func pduStatText(code int) string {
	switch code {
	case 0:
		return "REC UNREAD"
	case 1:
		return "REC READ"
	case 2:
		return "STO UNSENT"
	case 3:
		return "STO SENT"
	case 4:
		return "ALL"
	}
	return fmt.Sprintf("STAT %d", code)
}

// isHexLine проверяет, что строка — hex-последовательность
// (чётная длина, только hex-символы).
func isHexLine(s string) bool {
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

// ==================== Delete ====================

// DeleteSMS удаляет SMS по индексу.
func (m *Modem) DeleteSMS(ctx context.Context, index int) error {
	if _, err := m.Exec(ctx, fmt.Sprintf("AT+CMGD=%d", index)); err != nil {
		return fmt.Errorf("AT+CMGD=%d: %w", index, err)
	}
	return nil
}

// DeleteAllSMS удаляет все SMS из памяти модема.
func (m *Modem) DeleteAllSMS(ctx context.Context) error {
	if _, err := m.Exec(ctx, "AT+CMGD=1,4"); err != nil {
		return fmt.Errorf("AT+CMGD=1,4: %w", err)
	}
	return nil
}
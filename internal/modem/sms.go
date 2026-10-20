// Package modem, файл sms.go — высокоуровневые операции с SMS:
// отправка (в том числе multipart), чтение, удаление.
//
// Отправка работает в PDU-режиме (AT+CMGF=0) с использованием
// git.vidya.lv/vidya/sim800ctl/pkg/pdu. ForceUCS2 и RequestDR
// включаются по настройкам профиля.
package modem

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.vidya.lv/vidya/sim800ctl/pkg/pdu"
)

// SendOptions — параметры отправки SMS.
type SendOptions struct {
	// RequestDR — запросить delivery report у сети.
	// На SIM800L требуется также PreSendHook профиля (AT+CSMP).
	RequestDR bool
}

// SendResult — результат отправки.
//
// Для multipart содержит несколько references (по одному на часть).
// Поля Reference, TPDULength, RawPDU сохранены для обратной
// совместимости и содержат данные первой части.
type SendResult struct {
	// Общие.
	Parts int `json:"parts"` // число частей

	// Первая часть (для обратной совместимости).
	Reference  int64  `json:"reference"`   // TP-MR первой части
	TPDULength int    `json:"tpdu_length"` // длина TPDU первой части
	RawPDU     string `json:"raw_pdu"`     // hex PDU первой части

	// Все части.
	References []int64  `json:"references"` // TP-MR каждой части
	RawPDUs    []string `json:"raw_pdus"`   // hex PDU каждой части
}

// SendSMS собирает PDU и отправляет все части через AT+CMGS.
//
// Текст разбивается автоматически (GSM-7: 160/153, UCS-2: 70/67
// символов). Каждая часть отправляется отдельным AT+CMGS. Если
// любая часть падает, возвращается ошибка с указанием номера части;
// уже отправленные части отменить нельзя — это ограничение GSM.
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

	res := SendResult{
		Parts:      len(parts),
		References: make([]int64, 0, len(parts)),
		RawPDUs:    make([]string, 0, len(parts)),
	}

	// 4. Отправить все части последовательно.
	for i, part := range parts {
		cmd := fmt.Sprintf("AT+CMGS=%d", part.TpduLength)
		payload := []byte(part.Hex + "\x1a")

		resp, err := m.transport.ExecPrompt(ctx, cmd, payload)
		if err != nil {
			return res, fmt.Errorf("часть %d/%d: %w",
				i+1, len(parts), err)
		}
		ref, err := parseCMGSReference(resp.Payload())
		if err != nil {
			return res, fmt.Errorf("часть %d/%d: %w",
				i+1, len(parts), err)
		}
		res.References = append(res.References, ref)
		res.RawPDUs = append(res.RawPDUs, part.Hex)
	}

	// 5. Заполнить поля первой части для совместимости.
	res.Reference = res.References[0]
	res.TPDULength = parts[0].TpduLength
	res.RawPDU = parts[0].Hex

	m.logger.Info("modem: SMS отправлено",
		"peer", peer,
		"parts", len(parts),
		"references", res.References,
	)
	return res, nil
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

	// AlignReadStorage: SIM800L может хранить входящие в SM, а
	// читать из ME. Выравниваем перед чтением, если профиль требует.
	if err := m.maybeAlignStorage(ctx); err != nil {
		m.logger.Warn("modem: выравнивание storage не удалось",
			"err", err)
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

// ==================== Delete ====================

// DeleteSMS удаляет SMS по индексу.
func (m *Modem) DeleteSMS(ctx context.Context, index int) error {
	if err := m.maybeAlignStorage(ctx); err != nil {
		m.logger.Warn("modem: выравнивание storage не удалось",
			"err", err)
	}
	if _, err := m.Exec(ctx, fmt.Sprintf("AT+CMGD=%d", index)); err != nil {
		return fmt.Errorf("AT+CMGD=%d: %w", index, err)
	}
	return nil
}

// DeleteAllSMS удаляет все SMS из памяти модема.
func (m *Modem) DeleteAllSMS(ctx context.Context) error {
	if err := m.maybeAlignStorage(ctx); err != nil {
		m.logger.Warn("modem: выравнивание storage не удалось",
			"err", err)
	}
	if _, err := m.Exec(ctx, "AT+CMGD=1,4"); err != nil {
		return fmt.Errorf("AT+CMGD=1,4: %w", err)
	}
	return nil
}

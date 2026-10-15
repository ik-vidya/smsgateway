// Package modem, файл storage.go — выравнивание хранилища SMS.
//
// На SIM800L бывает, что модем сохраняет входящие в "SM", а читает
// из "ME". AT+CMGL тогда возвращает пустоту, хотя входящие SMS есть.
// Обход: перед чтением выставить read-storage = receive-storage.
//
// Управляется флагом Workarounds.AlignReadStorage профиля.
package modem

import (
	"context"
	"fmt"
	"regexp"
)

// cpmsState — разобранный ответ AT+CPMS?.
type cpmsState struct {
	Read    string // mem1
	Write   string // mem2
	Receive string // mem3
}

// reCPMS разбирает ответ вида:
//
//	+CPMS: "ME",0,50,"ME",0,50,"SM",0,25
var reCPMS = regexp.MustCompile(
	`\+CPMS:\s*"([^"]*)",\d+,\d+,"([^"]*)",\d+,\d+,"([^"]*)",\d+,\d+`)

// parseCPMS разбирает ответ AT+CPMS?.
func parseCPMS(resp string) cpmsState {
	m := reCPMS.FindStringSubmatch(resp)
	if len(m) < 4 {
		return cpmsState{}
	}
	return cpmsState{Read: m[1], Write: m[2], Receive: m[3]}
}

// maybeAlignStorage выравнивает read-storage с receive-storage,
// если этого требует профиль модема.
//
// Вызывается перед операциями чтения (AT+CMGL) и удаления (AT+CMGD),
// чтобы избежать ситуации "модем хранит в SM, а читаем из ME".
//
// Если профиль не требует, или storage уже выровнен — no-op.
func (m *Modem) maybeAlignStorage(ctx context.Context) error {
	if m.profile == nil {
		return nil
	}
	if !m.profile.Workarounds().AlignReadStorage {
		return nil
	}

	resp, err := m.Exec(ctx, "AT+CPMS?")
	if err != nil {
		return fmt.Errorf("AT+CPMS?: %w", err)
	}

	st := parseCPMS(resp)
	if st.Receive == "" {
		// Не смогли разобрать — не трогаем, пусть работает как есть.
		return nil
	}
	if st.Read == st.Receive {
		// Уже выровнено.
		return nil
	}

	m.logger.Info("modem: выравнивание storage",
		"read", st.Read,
		"receive", st.Receive,
	)

	if _, err := m.Exec(ctx, fmt.Sprintf(`AT+CPMS="%s"`, st.Receive)); err != nil {
		return fmt.Errorf(`AT+CPMS="%s": %w`, st.Receive, err)
	}
	return nil
}
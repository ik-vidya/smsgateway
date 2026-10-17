// Package service, файл network.go — опрос сетевого состояния
// (CREG, CSQ) и производные признаки.
//
// Зачем: networkStat и lastCSQ инициализируются в 0 и обновляются
// только через URC +CREG. Но URC +CREG не приходит, пока модем не
// получил AT+CREG=2 (а мы его не шлём в Init — сделаем отдельным
// коммитом). В результате после старта dashboard всегда показывал
// бы «не зарегистрирован» и «нет сигнала», даже на работающем
// модеме. Периодический опрос решает эту проблему без изменения
// Init: первый опрос — сразу после Init, дальше раз в 30 секунд.
package service

import (
	"context"
	"regexp"
	"strconv"
	"time"
)

// networkRefreshInterval — период опроса AT+CREG? и AT+CSQ.
//
// 30 секунд — компромисс: достаточно часто для актуального
// dashboard (пользователь обновляет страницу редко), достаточно
// редко, чтобы не гонять AT-команды без надобности.
const networkRefreshInterval = 30 * time.Second

var (
	// reCREGStat — Stat из +CREG: <n>,<stat>[,<lac>,<ci>].
	// Префикс +CGREG: тоже принимаем: если пользователь поменяет
	// команду на AT+CGREG?, парсинг продолжит работать.
	reCREGStat = regexp.MustCompile(`\+C(?:G)?REG:\s*\d+\s*,\s*(\d+)`)

	// reCSQ — rssi из +CSQ: <rssi>,<ber>.
	reCSQ = regexp.MustCompile(`\+CSQ:\s*(\d+)\s*,\s*\d+`)
)

// Registered сообщает, зарегистрирован ли модем в сети.
//
// CREG stat 1 (домашняя сеть) или 5 (роуминг) = да. Остальное
// (0 — нет, 2 — поиск, 3 — отказ, 4 — неизвестно) = нет.
//
// Это «истина» для dashboard: именно этот признак определяет,
// можно ли отправлять SMS. Порт открыт — ещё не значит, что
// модем в сети.
func (s *Service) Registered() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.networkStat == 1 || s.networkStat == 5
}

// networkLoop опрашивает CREG и CSQ периодически. Блокируется
// до отмены ctx.
func (s *Service) networkLoop(ctx context.Context) {
	defer s.wg.Done()
	s.logger.Info("service: цикл опроса сети запущен",
		"interval", networkRefreshInterval)

	s.refreshNetwork(ctx)

	tick := time.NewTicker(networkRefreshInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("service: цикл опроса сети остановлен")
			return
		case <-tick.C:
			s.refreshNetwork(ctx)
		}
	}
}

// refreshNetwork делает одну итерацию опроса.
//
// Ошибки не логируются как warn: modem.Exec уже логирует таймауты,
// а «CREG недоступен» на некоторых модемах — нормальное состояние
// (нет SIM, идёт reset). На следующем тике попробуем снова.
func (s *Service) refreshNetwork(ctx context.Context) {
	if s.modem == nil {
		return
	}

	if resp, err := s.modem.Exec(ctx, "AT+CREG?"); err == nil {
		if stat, ok := parseCREGStat(resp); ok {
			s.mu.Lock()
			changed := s.networkStat != stat
			s.networkStat = stat
			s.mu.Unlock()
			if changed {
				s.logger.Info("service: регистрация",
					"stat", stat, "text", interpretCREGStat(stat))
			}
		}
	}

	if resp, err := s.modem.Exec(ctx, "AT+CSQ"); err == nil {
		if csq, ok := parseCSQ(resp); ok {
			s.mu.Lock()
			s.lastCSQ = csq
			s.mu.Unlock()
		}
	}
}

// parseCREGStat извлекает Stat из ответа AT+CREG?.
//
// Формат: "+CREG: <n>,<stat>[,\"<lac>\",\"<ci>\"]".
// Возвращает (stat, true) при успехе.
func parseCREGStat(resp string) (int, bool) {
	m := reCREGStat.FindStringSubmatch(resp)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseCSQ извлекает rssi из ответа AT+CSQ.
//
// Формат: "+CSQ: <rssi>,<ber>". rssi=99 означает «неизвестно/
// не определяется» — это валидное значение, клиент решает сам,
// показывать его или нет.
func parseCSQ(resp string) (int, bool) {
	m := reCSQ.FindStringSubmatch(resp)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// interpretCREGStat — текстовое описание статуса регистрации
// (для логов). Не путать с diagnostics.interpretCREG — тот
// в пакете diagnostics и используется для JSON-отчёта.
func interpretCREGStat(stat int) string {
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
	return "неизвестный статус"
}

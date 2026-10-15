// Package modem, файл transport.go — низкоуровневый транспорт AT-команд
// через последовательный порт.
//
// Модель работы:
//   - Одна горутина-writer: сериализует запись в порт.
//   - Одна горутина-reader: читает строки, классифицирует их
//     (URC / эхо / ответ) и раскладывает по каналам.
//   - Exec(ctx, cmd): синхронный вызов команды, ждёт ответа в respCh,
//     не блокируя обработку URC.
//   - URCs(): канал URC для сервисного слоя.
//
// Это необходимо, потому что на SIM800L (и большинстве модемов) URC
// приходят асинхронно и могут вклиниваться в ответ на AT-команду.
package modem

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/ik-vidya/smsgateway/internal/profiles"
)

// Transport — минимальный интерфейс для последовательного порта.
// Реализуется go.bug.st/serial.Port, но абстракция позволяет
// подставить mock в тестах.
type Transport interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

// Config — параметры транспорта.
type Config struct {
	// CmdTimeout — таймаут ожидания ответа на AT-команду.
	CmdTimeout time.Duration

	// Profile — профиль модема. Определяет специфику разбора URC
	// и опциональные хуки.
	Profile profiles.Profile

	// Logger — логгер. Обычно slog.Default().
	Logger Logger
}

// Logger — минимальный интерфейс логгера, чтобы не тащить сюда slog.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// New создаёт транспорт на базе io.ReadWriteCloser.
// Читатель и писатель запускаются сразу; останавливаются по Close.
func New(t Transport, cfg Config) (*ATTransport, error) {
	if t == nil {
		return nil, errors.New("transport не задан")
	}
	if cfg.CmdTimeout <= 0 {
		cfg.CmdTimeout = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = nopLogger{}
	}
	if cfg.Profile == nil {
		cfg.Profile = profiles.Generic{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	a := &ATTransport{
		transport: t,
		cfg:       cfg,
		respCh:    make(chan string, 64),
		urcCh:     make(chan URC, 64),
		cmdMu:     sync.Mutex{},
		writerCh:  make(chan []byte, 16),
		closedCh:  make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		reader:    bufio.NewReaderSize(t, 4096),
	}
	a.wg.Add(2)
	go a.readerLoop()
	go a.writerLoop()
	return a, nil
}

// ATTransport — обёртка над Transport с разделением на respCh/urcCh.
type ATTransport struct {
	transport Transport
	cfg       Config
	reader    *bufio.Reader

	// Один Exec в любой момент времени; гарантирует, что ответы
	// одной команды не смешаются с другой.
	cmdMu sync.Mutex

	// Каналы.
	respCh  chan string // строки ответа текущей команды
	urcCh   chan URC    // асинхронные URC
	writerCh chan []byte

	// Жизненный цикл.
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	closedCh chan struct{}
	closeOnce sync.Once
}

// URCs возвращает канал URC. Канал закрывается при Close().
func (a *ATTransport) URCs() <-chan URC { return a.urcCh }

// Exec отправляет команду и ждёт ответа.
//
//   - Пишет cmd + "\r" в порт.
//   - Читает строки из respCh до терминатора:
//     "OK", "ERROR", "+CME ERROR: ...", "+CMS ERROR: ..."
//     или до истечения timeout.
//   - Возвращает собранные строки и ошибку, если терминатор — ERROR
//     или таймаут.
//
// URC, пришедшие во время выполнения, автоматически уходят в urcCh
// и не влияют на ответ.
func (a *ATTransport) Exec(ctx context.Context, cmd string) (Response, error) {
	select {
	case <-a.closedCh:
		return Response{}, errors.New("транспорт закрыт")
	default:
	}

	// Сериализация: одна команда за раз.
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	// Очистить возможные остатки от прошлых команд.
	a.drainRespCh()

	a.cfg.Logger.Debug("AT →", "cmd", cmd)
	if _, err := a.write([]byte(cmd + "\r")); err != nil {
		return Response{}, fmt.Errorf("запись команды %q: %w", cmd, err)
	}

	timeout := a.cfg.CmdTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d < timeout {
			timeout = d
		}
	}
	if timeout <= 0 {
		timeout = a.cfg.CmdTimeout
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	resp := Response{Command: cmd}
	for {
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-a.closedCh:
			return resp, errors.New("транспорт закрыт")
		case <-deadline.C:
			return resp, fmt.Errorf("таймаут %s ожидания ответа на %q",
				timeout, cmd)
		case line := <-a.respCh:
			a.cfg.Logger.Debug("AT ←", "line", line)
			if term, ok := classifyTerminator(line); ok {
				resp.Lines = append(resp.Lines, line)
				if term == termOK {
					return resp, nil
				}
				return resp, errorFromTerminator(line)
			}
			resp.Lines = append(resp.Lines, line)
		}
	}
}

// write отправляет байты в порт через канал writer.
func (a *ATTransport) write(data []byte) (int, error) {
	select {
	case a.writerCh <- data:
		return len(data), nil
	case <-a.closedCh:
		return 0, errors.New("транспорт закрыт")
	}
}

// Close останавливает reader/writer, закрывает каналы и порт.
// Идемпотентна.
//
// Порядок:
//  1. cancel() — сигнал для writer и reader (на случай, если они
//     проверяют контекст).
//  2. transport.Close() — заставляет базовый Read вернуть ошибку,
//     чтобы readerLoop вышел из блокирующего Read.
//  3. wg.Wait() — ждём завершения обеих горутин.
//  4. close(closedCh) — сигнал для Exec, что транспорт закрыт.
func (a *ATTransport) Close() error {
    var err error
    a.closeOnce.Do(func() {
	a.cancel()
	err = a.transport.Close()
	a.wg.Wait()
	close(a.closedCh)
    })
    return err
}

// ==================== горутины ====================

func (a *ATTransport) writerLoop() {
	defer a.wg.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case data := <-a.writerCh:
			if _, err := a.transport.Write(data); err != nil {
				a.cfg.Logger.Error("writer: запись", "err", err)
				return
			}
		}
	}
}

func (a *ATTransport) readerLoop() {
	defer a.wg.Done()
	defer close(a.respCh)
	defer close(a.urcCh)

	for {
		select {
		case <-a.ctx.Done():
			return
		default:
		}

		line, err := a.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				a.cfg.Logger.Debug("reader: EOF")
				return
			}
			select {
			case <-a.ctx.Done():
				return
			default:
			}
			a.cfg.Logger.Error("reader: чтение", "err", err)
			return
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		a.dispatch(line)
	}
}

// dispatch классифицирует строку: URC, эхо или ответ.
func (a *ATTransport) dispatch(line string) {
	// 1. URC?
	if urc, ok := a.tryParseURC(line); ok {
		select {
		case a.urcCh <- urc:
		case <-a.ctx.Done():
		}
		return
	}

	// 2. Ответ на команду.
	select {
	case a.respCh <- line:
	case <-a.ctx.Done():
	}
}

// tryParseURC пытается распознать строку как URC.
//
// Сначала пробуем специфичный парсер профиля (для нестандартных URC),
// потом стандартные.
func (a *ATTransport) tryParseURC(line string) (URC, bool) {
	// Специфичные URC профиля — если профиль это умеет.
	// (Интерфейс profiles.Profile пока не содержит ParseURC, но
	// зарезервировано на будущее.)
	_ = a.cfg.Profile

	// Стандартные URC.
	return parseStandardURC(line)
}

// drainRespCh опустошает respCh перед новой командой.
func (a *ATTransport) drainRespCh() {
	for {
		select {
		case <-a.respCh:
		default:
			return
		}
	}
}
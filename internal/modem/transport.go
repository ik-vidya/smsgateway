// Package modem, файл transport.go — низкоуровневый транспорт AT-команд
// через последовательный порт.
//
// Модель работы:
//   - Одна горутина-reader: побайтово читает порт, собирает строки,
//     распознаёт URC, эхо, prompt "> " и обычные ответы.
//   - Exec(ctx, cmd): синхронный вызов, ждёт терминатор (OK/ERROR/
//     +CME ERROR/+CMS ERROR). URC и prompt не попадают в ответ.
//   - ExecPrompt(ctx, cmd, payload): для AT+CMGS — ждёт "> ",
//     пишет payload, продолжает ждать терминатор.
//   - URCs(): канал URC для сервисного слоя.
//
// Запись в порт синхронная. Все команды сериализуются через cmdMu,
// поэтому одной горутины-писателя не требуется.
package modem

import (
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
type Transport interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

// Config — параметры транспорта.
type Config struct {
	CmdTimeout time.Duration
	Profile    profiles.Profile
	Logger     Logger
}

// Logger — минимальный интерфейс логгера.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// New создаёт транспорт на базе Transport.
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
		closedCh:  make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
	}
	a.wg.Add(1)
	go a.readerLoop()
	return a, nil
}

// ATTransport — обёртка над Transport.
type ATTransport struct {
	transport Transport
	cfg       Config

	// Один Exec в любой момент времени.
	cmdMu sync.Mutex

	// Каналы.
	respCh chan string
	urcCh  chan URC

	// Жизненный цикл.
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closedCh  chan struct{}
	closeOnce sync.Once
}

// URCs возвращает канал URC.
func (a *ATTransport) URCs() <-chan URC { return a.urcCh }

// Exec отправляет команду и ждёт ответа.
func (a *ATTransport) Exec(ctx context.Context, cmd string) (Response, error) {
	return a.exec(ctx, cmd, nil, false)
}

// ExecPrompt отправляет команду, ждёт prompt "> ", пишет payload,
// и ждёт терминатор. Используется для AT+CMGS.
func (a *ATTransport) ExecPrompt(ctx context.Context, cmd string, payload []byte) (Response, error) {
	return a.exec(ctx, cmd, payload, true)
}

// exec — общий путь для Exec и ExecPrompt.
func (a *ATTransport) exec(ctx context.Context, cmd string, payload []byte, waitPrompt bool) (Response, error) {
	select {
	case <-a.closedCh:
		return Response{}, errors.New("транспорт закрыт")
	default:
	}

	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	a.drainRespCh()

	a.cfg.Logger.Debug("AT →", "cmd", cmd)
	if err := a.writeString(cmd + "\r"); err != nil {
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

	resp := Response{Command: cmd}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	// Фаза 1 (только для waitPrompt): ждём "> ".
	if waitPrompt {
		gotPrompt := false
		for !gotPrompt {
			select {
			case <-ctx.Done():
				return resp, ctx.Err()
			case <-a.closedCh:
				return resp, errors.New("транспорт закрыт")
			case <-deadline.C:
				return resp, fmt.Errorf("таймаут %s ожидания prompt на %q",
					timeout, cmd)
			case line, ok := <-a.respCh:
				if !ok {
					return resp, errors.New("канал ответов закрыт")
				}
				a.cfg.Logger.Debug("AT ←", "line", line)
				if isPrompt(line) {
					gotPrompt = true
					continue
				}
				if term, ok := classifyTerminator(line); ok {
					resp.Lines = append(resp.Lines, line)
					if term == termOK {
						return resp, nil
					}
					return resp, errorFromTerminator(line)
				}
			}
		}
		a.cfg.Logger.Debug("AT →", "payload_bytes", len(payload))
		if err := a.writeBytes(payload); err != nil {
			return resp, fmt.Errorf("запись payload: %w", err)
		}
	}

	// Фаза 2: ждём терминатор.
	for {
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-a.closedCh:
			return resp, errors.New("транспорт закрыт")
		case <-deadline.C:
			return resp, fmt.Errorf("таймаут %s ожидания ответа на %q",
				timeout, cmd)
		case line, ok := <-a.respCh:
			if !ok {
				return resp, errors.New("канал ответов закрыт")
			}
			a.cfg.Logger.Debug("AT ←", "line", line)
			if isPrompt(line) {
				continue
			}
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

// isPrompt сообщает, является ли строка prompt-приглашением модема.
func isPrompt(line string) bool {
	return line == "> " || line == ">"
}

// writeString пишет строку в порт синхронно.
func (a *ATTransport) writeString(s string) error {
	return a.writeBytes([]byte(s))
}

// writeBytes пишет байты в порт синхронно.
//
// Синхронность важна для тестов и для корректного порядка: после
// возврата из writeBytes данные гарантированно переданы в порт.
func (a *ATTransport) writeBytes(data []byte) error {
	select {
	case <-a.closedCh:
		return errors.New("транспорт закрыт")
	default:
	}
	if _, err := a.transport.Write(data); err != nil {
		return err
	}
	return nil
}

// Close останавливает reader, закрывает каналы и порт.
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

// ==================== reader ====================

// readerLoop читает порт побайтово, собирает строки по \n и
// распознаёт prompt "> " (без \n).
//
// Побайтовое чтение выбрано ради prompt: модем присылает "> " без
// перевода строки и сразу ждёт PDU, поэтому построчный ReadString
// заблокировался бы навсегда.
func (a *ATTransport) readerLoop() {
	defer a.wg.Done()
	defer close(a.respCh)
	defer close(a.urcCh)

	buf := make([]byte, 0, 256)
	tmp := make([]byte, 1)

	for {
		select {
		case <-a.ctx.Done():
			return
		default:
		}

		n, err := a.transport.Read(tmp)
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
		if n == 0 {
			continue
		}

		c := tmp[0]
		buf = append(buf, c)

		// Проверка prompt: "> " в конце буфера (без \n).
		if len(buf) >= 2 && buf[len(buf)-2] == '>' && buf[len(buf)-1] == ' ' {
			select {
			case a.respCh <- "> ":
			case <-a.ctx.Done():
				return
			}
			buf = buf[:0]
			continue
		}

		// Конец строки.
		if c == '\n' {
			line := strings.TrimRight(string(buf), "\r\n")
			buf = buf[:0]
			if line == "" {
				continue
			}
			a.dispatch(line)
		}
	}
}

// dispatch классифицирует строку: URC или ответ.
func (a *ATTransport) dispatch(line string) {
	if urc, ok := a.tryParseURC(line); ok {
		select {
		case a.urcCh <- urc:
		case <-a.ctx.Done():
		}
		return
	}
	select {
	case a.respCh <- line:
	case <-a.ctx.Done():
	}
}

// tryParseURC пытается распознать строку как URC.
func (a *ATTransport) tryParseURC(line string) (URC, bool) {
	_ = a.cfg.Profile
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
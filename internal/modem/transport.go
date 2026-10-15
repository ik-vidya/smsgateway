// Package modem, файл transport.go — низкоуровневый транспорт AT-команд
// через последовательный порт.
//
// Модель работы:
//   - Одна горутина-reader: побайтово читает порт, собирает строки,
//     распознаёт URC, эхо, prompt "> " и обычные ответы. Для +CDS
//     читает следующую строку как hex-PDU отчёта.
//   - Exec(ctx, cmd): синхронный вызов, ждёт терминатор.
//   - ExecPrompt(ctx, cmd, payload): для AT+CMGS.
//   - URCs(): канал URC для сервисного слоя.
//
// Запись в порт синхронная. Все команды сериализуются через cmdMu,
// поэтому отдельной writer-горутины не требуется.
package modem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/ik-vidya/sim800ctl/pkg/pdu"
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

	cmdMu sync.Mutex

	respCh chan string
	urcCh  chan URC

	// pendingCDS — состояние reader-горутины: получен +CDS header,
	// ждём hex-PDU. При получении PDU URC обогащается и уходит
	// в urcCh.
	pendingCDS *URCCDS

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

func isPrompt(line string) bool {
	return line == "> " || line == ">"
}

func (a *ATTransport) writeString(s string) error {
	return a.writeBytes([]byte(s))
}

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

		// Prompt "> " — без \n.
		if len(buf) >= 2 && buf[len(buf)-2] == '>' && buf[len(buf)-1] == ' ' {
			select {
			case a.respCh <- "> ":
			case <-a.ctx.Done():
				return
			}
			buf = buf[:0]
			continue
		}

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
//
// Особая логика для +CDS: после заголовка "+CDS: <len>" ожидается
// hex-PDU в следующей строке. Пока PDU не получен, URC не уходит
// в urcCh — он копится в a.pendingCDS.
func (a *ATTransport) dispatch(line string) {
	// 1. Ждём PDU после +CDS.
	if a.pendingCDS != nil {
		if isHexLine(line) {
			a.enrichCDS(a.pendingCDS, line)
			select {
			case a.urcCh <- *a.pendingCDS:
			case <-a.ctx.Done():
			}
			a.pendingCDS = nil
			return
		}
		// Не hex — значит PDU не пришёл. Отправляем URC без PDU
		// и обрабатываем текущую строку как обычно.
		select {
		case a.urcCh <- *a.pendingCDS:
		case <-a.ctx.Done():
		}
		a.pendingCDS = nil
		// fallthrough
	}

	// 2. Обычная классификация.
	if urc, ok := a.tryParseURC(line); ok {
		if cds, isCDS := urc.(URCCDS); isCDS {
			// +CDS header — ждём PDU.
			cp := cds
			a.pendingCDS = &cp
			return
		}
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

// enrichCDS декодирует PDU отчёта и заполняет поля URCCDS.
func (a *ATTransport) enrichCDS(cds *URCCDS, pduHex string) {
	cds.PDU = pduHex
	d, err := pdu.Decode(pduHex)
	if err != nil {
		a.cfg.Logger.Warn("+CDS: PDU decode", "err", err, "pdu", pduHex)
		return
	}
	if d.Type != "status-report" {
		a.cfg.Logger.Warn("+CDS: PDU не status-report", "type", d.Type)
		return
	}
	cds.Reference = d.Reference
	cds.TPST = d.Status
	cds.Peer = d.Peer
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
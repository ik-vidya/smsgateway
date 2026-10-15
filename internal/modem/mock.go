// Package modem, файл mock.go — mock-транспорт для тестов.
//
// Реализован через sync.Cond + bytes.Buffer. Reader блокируется на
// cond.Wait() до появления данных. Писатель добавляет данные и
// вызывает cond.Broadcast(). Все записи в порт дополнительно
// копятся в writeBuf для проверки в тестах через WrittenBytes().
package modem

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// MockTransport — io.ReadWriter для тестов.
type MockTransport struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      bytes.Buffer // данные, ожидающие чтения
	writeBuf bytes.Buffer // история всего, что было записано
	script   []mockResponse
	closed   bool
}

type mockResponse struct {
	// matchCmd — команда (без \r), при получении которой отдать
	// эту порцию ответа. Пусто = отдать при следующей записи.
	matchCmd string
	// lines — строки ответа (без \r\n).
	lines []string
	// delayMs — задержка перед отправкой (мс).
	delayMs int
}

// NewMock создаёт mock-транспорт без ответов.
func NewMock() *MockTransport {
	m := &MockTransport{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// PushResponse добавляет ответ на команду.
func (m *MockTransport) PushResponse(cmd string, lines ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.script = append(m.script, mockResponse{matchCmd: cmd, lines: lines})
}

// PushResponseDelayed добавляет ответ с задержкой.
func (m *MockTransport) PushResponseDelayed(cmd string, delayMs int, lines ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.script = append(m.script, mockResponse{
		matchCmd: cmd, lines: lines, delayMs: delayMs,
	})
}

// PushUnsolicited добавляет URC, который будет отдан при следующей
// записи в порт (независимо от команды).
func (m *MockTransport) PushUnsolicited(lines ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.script = append(m.script, mockResponse{matchCmd: "", lines: lines})
}

// Read читает из внутреннего буфера. Блокируется до появления
// данных или Close. Возвращает io.EOF после Close, если буфер пуст.
func (m *MockTransport) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for m.buf.Len() == 0 && !m.closed {
		m.cond.Wait()
	}
	if m.buf.Len() == 0 {
		return 0, io.EOF
	}
	return m.buf.Read(p)
}

// Write принимает команду и подбирает ответ по скрипту.
//
// Сначала ищет точное совпадение по matchCmd, затем первый
// unsolicited (matchCmd == "").
//
// ВАЖНО: found копируется по значению (а не по указателю на элемент
// slice), потому что append(m.script[:i], m.script[i+1:]...) сдвигает
// элементы in-place, и указатель на m.script[i] после этого указывает
// уже на другой ответ.
func (m *MockTransport) Write(p []byte) (int, error) {
	cmd := string(bytes.TrimRight(p, "\r\n"))

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	m.writeBuf.Write(p)

	var found mockResponse
	var foundOK bool

	// 1. Точное совпадение по команде.
	for i := range m.script {
		r := m.script[i]
		if r.matchCmd == cmd {
			found = r
			foundOK = true
			m.script = append(m.script[:i], m.script[i+1:]...)
			break
		}
	}
	// 2. Если нет — первый unsolicited.
	if !foundOK {
		for i := range m.script {
			r := m.script[i]
			if r.matchCmd == "" {
				found = r
				foundOK = true
				m.script = append(m.script[:i], m.script[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()

	if foundOK {
		go m.deliver(found)
	}
	return len(p), nil
}

// deliver добавляет строки в буфер и будит читателя.
func (m *MockTransport) deliver(r mockResponse) {
	if r.delayMs > 0 {
		time.Sleep(time.Duration(r.delayMs) * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	for _, line := range r.lines {
		m.buf.WriteString(line)
		m.buf.WriteString("\r\n")
	}
	m.cond.Broadcast()
}

// Close помечает mock закрытым и будит читателя.
// Reader после этого получит io.EOF.
func (m *MockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	m.cond.Broadcast()
	return nil
}

// WrittenBytes возвращает всё, что было записано в порт.
// Полезно в тестах для проверки, что определённая AT-команда
// была отправлена.
func (m *MockTransport) WrittenBytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.writeBuf.Bytes()...)
}
// Package modem, файл serial.go — открытие реального
// последовательного порта через go.bug.st/serial.
//
// Используется в cmd/smsgateway при старте сервиса. Если порт
// не открывается, main.go может работать в режиме "без модема"
// (см. ModemMode в config).
package modem

import (
	"fmt"
	"time"

	"go.bug.st/serial"
)

// SerialConfig — параметры открытия порта.
type SerialConfig struct {
	Port     string        // /dev/ttyUSB0 или COM3
	Baud     int           // 115200 и т.п.
	Timeout  time.Duration // read timeout; 100мс — разумное значение
	DataBits int           // 8
	Parity   serial.Parity // обычно serial.NoParity
	StopBits serial.StopBits
}

func (c SerialConfig) withDefaults() SerialConfig {
	if c.Baud <= 0 {
		c.Baud = 115200
	}
	if c.Timeout <= 0 {
		c.Timeout = 100 * time.Millisecond
	}
	if c.DataBits <= 0 {
		c.DataBits = 8
	}
	if c.Parity == 0 {
		c.Parity = serial.NoParity
	}
	if c.StopBits == 0 {
		c.StopBits = serial.OneStopBit
	}
	return c
}

// OpenSerial открывает последовательный порт.
//
// Возвращает serial.Port, который реализует интерфейс Transport
// (Read/Write/Close) — можно передать напрямую в New().
func OpenSerial(cfg SerialConfig) (serial.Port, error) {
	cfg = cfg.withDefaults()

	mode := &serial.Mode{
		BaudRate: cfg.Baud,
		DataBits: cfg.DataBits,
		Parity:   cfg.Parity,
		StopBits: cfg.StopBits,
	}

	port, err := serial.Open(cfg.Port, mode)
	if err != nil {
		return nil, fmt.Errorf("открытие порта %q: %w", cfg.Port, err)
	}
	if err := port.SetReadTimeout(cfg.Timeout); err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("SetReadTimeout: %w", err)
	}
	return port, nil
}

// ListSerialPorts возвращает список доступных портов.
// Полезно для команды `smsgateway list-ports`.
func ListSerialPorts() ([]string, error) {
	ports, err := serial.GetPortsList()
	if err != nil {
		return nil, fmt.Errorf("GetPortsList: %w", err)
	}
	return ports, nil
}
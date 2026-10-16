// Package modem, файл discover.go — обнаружение портов и
// идентификация модемов.
//
// Задача: для каждого последовательного порта собрать:
//
//   - путь устройства (/dev/ttyUSB0, COM3);
//   - метаданные USB из /sys/class/tty/<name>/device/ (VID, PID,
//     серийный номер, производитель, продукт);
//   - стабильные пути из /dev/serial/by-id/ и /dev/serial/by-path/
//     (если есть): by-id привязан к устройству, by-path — к порту;
//   - ответы модема на AT, AT+CGMI, AT+CGMM, AT+CGMR, AT+CGSN.
//
// Стабильные пути — ключ к запуску нескольких инстансов
// smsgateway на одной машине: /dev/ttyUSB* переезжает между
// перезагрузками, а /dev/serial/by-id/usb-Silicon_Labs_...-if00-port0
// не меняется, если модем тот же.
package modem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PortInfo — информация об одном порте и (возможно) модеме.
type PortInfo struct {
	// Порт — путь устройства.
	Port string `json:"port"`

	// StablePath — /dev/serial/by-id/* (если есть).
	// Привязан к конкретному устройству (по серийному номеру).
	StablePath string `json:"stable_path,omitempty"`

	// ByPathPath — /dev/serial/by-path/* (если есть).
	// Привязан к физическому USB-порту, не к устройству.
	ByPathPath string `json:"by_path_path,omitempty"`

	// Метаданные из /sys.
	VID          string `json:"vid,omitempty"`
	PID          string `json:"pid,omitempty"`
	SerialNumber string `json:"serial,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`

	// Ответы модема.
	ATResponse    string `json:"at,omitempty"`
	ModemMaker    string `json:"modem_maker,omitempty"`    // AT+CGMI
	ModemModel    string `json:"modem_model,omitempty"`    // AT+CGMM
	ModemRevision string `json:"modem_revision,omitempty"` // AT+CGMR
	IMEI          string `json:"imei,omitempty"`           // AT+CGSN

	// ATError — если опрос был, но не удался.
	ATError string `json:"at_error,omitempty"`

	// Skipped — AT-опрос пропущен (SkipAT=true или порт занят).
	Skipped bool `json:"skipped,omitempty"`
}

// DiscoverConfig — параметры обнаружения.
type DiscoverConfig struct {
	// Root — корень файловой системы. nil — os.DirFS("/").
	// Используется только для чтения /sys и /dev/serial.
	// Тесты подставляют временную директорию.
	Root fs.FS

	// Paths — список портов. nil — serial.GetPortsList().
	// Тесты подставляют фиксированный список.
	Paths []string

	// ATTimeout — таймаут на одну AT-команду. 0 — 2s.
	ATTimeout time.Duration

	// SkipAT — не опрашивать модем. Полезно, когда порт занят
	// сервисом или не хочется дёргать модем.
	SkipAT bool
}

// DiscoverPorts собирает PortInfo для всех доступных портов.
//
// Ошибка чтения /sys или /dev/serial/by-id не фатальна: если
// метаданные недоступны, соответствующее поле остаётся пустым.
// Фатальны только ошибки получения списка портов.
func DiscoverPorts(ctx context.Context, cfg DiscoverConfig) ([]PortInfo, error) {
	root := cfg.Root
	if root == nil {
		root = os.DirFS("/")
	}

	paths := cfg.Paths
	if paths == nil {
		var err error
		paths, err = ListSerialPorts()
		if err != nil {
			return nil, fmt.Errorf("список портов: %w", err)
		}
	}
	sort.Strings(paths)

	stableIndex := buildStableIndex(root)

	infos := make([]PortInfo, 0, len(paths))
	for _, p := range paths {
		info := PortInfo{Port: p}

		base := filepath.Base(p)
		readSysMetadata(root, base, &info)

		if s, ok := stableIndex[p]; ok {
			info.StablePath = s.byID
			info.ByPathPath = s.byPath
		}

		if !cfg.SkipAT {
			timeout := cfg.ATTimeout
			if timeout <= 0 {
				timeout = 2 * time.Second
			}
			if err := probeAT(ctx, p, timeout, &info); err != nil {
				info.ATError = err.Error()
			}
		} else {
			info.Skipped = true
		}

		infos = append(infos, info)
	}
	return infos, nil
}

// readSysMetadata читает /sys/class/tty/<name>/device/*.
// Молча пропускает отсутствующие файлы: на виртуальных портах
// (pty, COM) их не будет.
func readSysMetadata(root fs.FS, base string, info *PortInfo) {
	prefix := "sys/class/tty/" + base + "/device/"
	info.VID = readFileTrim(root, prefix+"idVendor")
	info.PID = readFileTrim(root, prefix+"idProduct")
	info.SerialNumber = readFileTrim(root, prefix+"serial")
	info.Manufacturer = readFileTrim(root, prefix+"manufacturer")
	info.Product = readFileTrim(root, prefix+"product")
}

func readFileTrim(fsys fs.FS, path string) string {
	b, err := fs.ReadFile(fsys, path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// stableNames — что нашлось в /dev/serial/by-id и by-path.
type stableNames struct {
	byID   string
	byPath string
}

// buildStableIndex читает /dev/serial/by-id и /dev/serial/by-path,
// резолвит симлинки и строит индекс real-path → stable-names.
//
// Внутри fs.FS симлинки представлены файлами, содержимое которых —
// цель ссылки. Цель может быть абсолютной (/dev/ttyUSB0) или
// относительной (../../ttyUSB0): во втором случае путь разрешается
// относительно директории самого симлинка.
func buildStableIndex(root fs.FS) map[string]stableNames {
	idx := make(map[string]stableNames)

	addDir := func(dir string, pick func(*stableNames) *string) {
		entries, err := fs.ReadDir(root, dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			full := dir + "/" + e.Name()
			target, err := fs.ReadFile(root, full)
			if err != nil {
				continue
			}
			real := resolveSymlinkTarget(dir, string(target))
			if real == "" {
				continue
			}

			st := idx[real]
			*pick(&st) = "/dev/serial/" + filepath.Base(dir) + "/" + e.Name()
			idx[real] = st
		}
	}

	addDir("dev/serial/by-id", func(s *stableNames) *string { return &s.byID })
	addDir("dev/serial/by-path", func(s *stableNames) *string { return &s.byPath })

	return idx
}

// resolveSymlinkTarget разрешает цель симлинка относительно
// директории symlinkDir. Возвращает абсолютный путь вида
// /dev/ttyUSB0 или "" для пустой цели.
//
// Примеры (symlinkDir = "dev/serial/by-id"):
//
//	"/dev/ttyUSB0"        → "/dev/ttyUSB0"
//	"../../ttyUSB0"       → "/dev/ttyUSB0"
//	"../../../dev/ttyUSB0" → "/dev/ttyUSB0"  (path.Clean схлопнет ..)
//	"../ttyUSB1"          → "/dev/serial/ttyUSB1"
func resolveSymlinkTarget(symlinkDir, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	var abs string
	if strings.HasPrefix(target, "/") {
		abs = target
	} else {
		abs = "/" + path.Join(symlinkDir, target)
	}
	return path.Clean(abs)
}

// probeAT опрашивает модем несколькими AT-командами.
//
// Использует транспорт modem.New напрямую, без Modem.Init:
// при обзоре портов не нужны ни PIN-unlock, ни CFUN, ни CNMI.
// Открытие порта может упасть, если порт занят (например,
// уже запущенным smsgateway) — тогда в info.ATError пишется
// причина, без ошибки наружу.
func probeAT(ctx context.Context, port string, timeout time.Duration,
	info *PortInfo) error {

	p, err := OpenSerial(SerialConfig{Port: port})
	if err != nil {
		return fmt.Errorf("открытие: %w", err)
	}
	defer func() { _ = p.Close() }()

	at, err := New(p, Config{CmdTimeout: timeout})
	if err != nil {
		return fmt.Errorf("транспорт: %w", err)
	}
	defer func() { _ = at.Close() }()

	// Проба связи.
	if _, err := at.Exec(ctx, "AT"); err != nil {
		return fmt.Errorf("AT: %w", err)
	}

	info.ATResponse = "OK"
	info.ModemMaker = firstPayloadStr(ctx, at, "AT+CGMI")
	info.ModemModel = firstPayloadStr(ctx, at, "AT+CGMM")
	info.ModemRevision = firstPayloadStr(ctx, at, "AT+CGMR")
	info.IMEI = firstPayloadStr(ctx, at, "AT+CGSN")
	return nil
}

func firstPayloadStr(ctx context.Context, at *ATTransport, cmd string) string {
	resp, err := at.Exec(ctx, cmd)
	if err != nil {
		return ""
	}
	for _, line := range resp.Payload() {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// ErrNoPorts — ни одного подходящего порта не найдено.
var ErrNoPorts = errors.New("последовательные порты не найдены")

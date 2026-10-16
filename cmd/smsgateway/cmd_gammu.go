// Команда smsgateway, файл cmd_gammu.go — подкоманда gammu-outbox.
//
// Разовый прогон outbox-директории Gammu: в отличие от сервиса,
// который следит за директорией в фоне, эта команда делает один
// проход и завершается. Полезна для отладки и для запуска из cron.
//
// Режим --dry-run не требует ни БД, ни модема: печатает, что
// было бы отправлено.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/gammu"
	"github.com/ik-vidya/smsgateway/internal/logging"
	"github.com/ik-vidya/smsgateway/internal/modem"
	"github.com/ik-vidya/smsgateway/internal/profiles"
	"github.com/ik-vidya/smsgateway/internal/service"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

func cmdGammuOutbox() *cobra.Command {
	var dryRun bool

	c := &cobra.Command{
		Use:   "gammu-outbox",
		Short: "Разовый прогон outbox-директории Gammu",
		Long: `Проходит по {GAMMU_ROOT}/outbox, отправляет все найденные
OUT*.txt файлы и завершается. В отличие от сервиса, который
следит за директорией в фоне, эта команда делает один проход.

При --dry-run только парсит файлы и печатает план, не открывая
ни модем, ни БД.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runGammuOutbox(dryRun)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false,
		"только показать план, не отправлять")
	return c
}

func runGammuOutbox(dryRun bool) error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}

	if !cfg.GammuEnabled() {
		return fmt.Errorf("GAMMU_ROOT не задан в конфиге")
	}

	if dryRun {
		return dryRunOutbox(cfg)
	}

	ctx := context.Background()

	// --- БД ---
	if err := ensureDir(cfg.DBPath); err != nil {
		return err
	}
	store, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("открытие БД: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			logger.Error("закрытие БД", "err", cerr)
		}
	}()

	// --- Модем (обязателен для реальной отправки) ---
	if cfg.SerialPort == "" || cfg.SerialPort == "none" {
		return fmt.Errorf("SERIAL_PORT не задан: для отправки нужен модем")
	}
	profile, err := profiles.Get(cfg.ModemProfile)
	if err != nil {
		return fmt.Errorf("профиль модема: %w", err)
	}
	port, err := modem.OpenSerial(modem.SerialConfig{
		Port: cfg.SerialPort,
		Baud: cfg.SerialBaud,
	})
	if err != nil {
		return fmt.Errorf("открытие serial: %w", err)
	}
	m, err := modem.NewModem(port, profile,
		cfg.ModemCmdTimeout, slogAdapter{logger})
	if err != nil {
		_ = port.Close()
		return fmt.Errorf("создание Modem: %w", err)
	}
	defer func() {
		if cerr := m.Close(); cerr != nil {
			logger.Error("закрытие модема", "err", cerr)
		}
	}()
	if cfg.ModemPIN != "" {
		m.SetPIN(cfg.ModemPIN)
	}
	if err := m.Init(ctx); err != nil {
		return fmt.Errorf("инициализация модема: %w", err)
	}

	// --- InboxWriter и Manager ---
	inbox, err := gammu.NewInboxWriter(
		cfg.GammuInboxPath(), cfg.GammuInboxFormat, logger)
	if err != nil {
		return fmt.Errorf("gammu inbox: %w", err)
	}
	mgr, err := gammu.NewManager(gammu.ManagerConfig{
		InboxDir:      cfg.GammuInboxPath(),
		OutboxDir:     cfg.GammuOutboxPath(),
		SentDir:       cfg.GammuSentPath(),
		ErrorDir:      cfg.GammuErrorPath(),
		InboxFormat:   cfg.GammuInboxFormat,
		WatchInterval: cfg.GammuWatchInterval,
		MaxAttempts:   cfg.GammuOutboxMaxAttempts,
	}, service.NewModemSender(m, logger),
		service.NewStorageOutboxStore(store), inbox, logger)
	if err != nil {
		return fmt.Errorf("gammu manager: %w", err)
	}

	st, err := mgr.SendNow(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("scanned=%d sent=%d retried=%d failed=%d skipped=%d\n",
		st.Scanned, st.Sent, st.Retried, st.Failed, st.Skipped)
	return nil
}

// dryRunOutbox печатает, что было бы отправлено. Не требует
// ни БД, ни модема — только файлы в outbox.
func dryRunOutbox(cfg *config.Config) error {
	dir := cfg.GammuOutboxPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("outbox не существует: %s\n", dir)
			return nil
		}
		return fmt.Errorf("чтение %q: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "OUT") {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	if len(names) == 0 {
		fmt.Println("outbox пуст.")
		return nil
	}

	fmt.Printf("outbox: %s\n", dir)
	for _, n := range names {
		full := filepath.Join(dir, n)
		fn, err := gammu.ParseOutgoingFileName(n)
		if err != nil {
			fmt.Printf("  %s\n    НЕВАЛИДНОЕ ИМЯ: %v\n", n, err)
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			fmt.Printf("  %s\n    чтение: %v\n", n, err)
			continue
		}
		content, err := gammu.DecodeText(raw)
		if err != nil {
			fmt.Printf("  %s\n    декодирование: %v\n", n, err)
			continue
		}
		msg, err := gammu.ParseFile(content)
		if err != nil {
			fmt.Printf("  %s\n    парсинг: %v\n", n, err)
			continue
		}
		text, src := msg.EffectiveText()
		recipient := msg.Number
		if recipient == "" {
			recipient = fn.Recipient
		}
		fmt.Printf("  %s\n", n)
		fmt.Printf("    prio=%s peer=%s dr=%v flash=%v source=%s\n",
			fn.Priority, recipient, fn.RequestDR, fn.Flash, src)
		fmt.Printf("    text=%q\n", text)
	}
	return nil
}

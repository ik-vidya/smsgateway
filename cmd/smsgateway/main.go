// Команда smsgateway — SMS-шлюз для AT-совместимых GSM-модемов.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ik-vidya/smsgateway/internal/api"
	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/gammu"
	"github.com/ik-vidya/smsgateway/internal/logging"
	"github.com/ik-vidya/smsgateway/internal/modem"
	"github.com/ik-vidya/smsgateway/internal/notify"
	"github.com/ik-vidya/smsgateway/internal/profiles"
	"github.com/ik-vidya/smsgateway/internal/service"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

var (
	flagConfig      string
	flagShowVersion bool
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "smsgateway",
	Short: "SMS-шлюз для AT-совместимых GSM-модемов",
	Long: `smsgateway — долгоживущий сервис приёма и отправки SMS через
AT-совместимый GSM-модем. Предоставляет HTTP/JSON REST API, сохраняет
сообщения в SQLite, работает с Gammu file backend.

Без подкоманды запускает сервис. Требуется флаг --config.

Если SERIAL_PORT пуст или = "none", сервис запускается в режиме
"без модема": API принимает сообщения в очередь, но не отправляет.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runService,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "",
		"путь к .env-файлу (обязателен для всех команд, кроме version и list-ports)")
	rootCmd.Flags().BoolVarP(&flagShowVersion, "version", "v", false,
		"показать версию и выйти")

	rootCmd.AddCommand(
		cmdVersion(),
		cmdMigrate(),
		cmdListPorts(),
		cmdGammuOutbox(),
		cmdConvertGammuConfig(),
	)
}

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Показать версию и выйти",
		Run: func(_ *cobra.Command, _ []string) {
			printVersion()
		},
	}
}

func printVersion() {
	fmt.Printf("smsgateway %s (сборка %s)\n", version, buildTime)
}

func cmdListPorts() *cobra.Command {
	return &cobra.Command{
		Use:   "list-ports",
		Short: "Показать доступные последовательные порты",
		RunE: func(_ *cobra.Command, _ []string) error {
			ports, err := modem.ListSerialPorts()
			if err != nil {
				return err
			}
			if len(ports) == 0 {
				fmt.Println("Порты не найдены.")
				return nil
			}
			for _, p := range ports {
				fmt.Println(p)
			}
			return nil
		},
	}
}

func cmdMigrate() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Применить миграции БД и выйти",
		RunE:  runMigrate,
	}
}

func runMigrate(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}

	logger.Info("миграция БД", "db_path", cfg.DBPath)

	if err := ensureDir(cfg.DBPath); err != nil {
		return err
	}

	ctx := context.Background()
	store, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("открытие БД: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			logger.Error("закрытие БД", "err", cerr)
		}
	}()

	logger.Info("миграции применены")
	return nil
}

func runService(_ *cobra.Command, _ []string) error {
	if flagShowVersion {
		printVersion()
		return nil
	}

	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}

	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}

	// --- Notifier (Telegram или Noop) ---
	var notifier notify.Notifier = notify.Noop{}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		tg, err := notify.NewTelegram(notify.TelegramConfig{
			BotToken: cfg.TelegramBotToken,
			ChatID:   cfg.TelegramChatID,
			Logger:   logger,
		})
		if err != nil {
			logger.Error("не удалось создать Telegram-нотификатор",
				"err", err)
		} else {
			notifier = tg
			logger.Info("Telegram-нотификатор активен")
		}
	}
	defer func() {
		if cerr := notifier.Close(); cerr != nil {
			logger.Error("закрытие notifier", "err", cerr)
		}
	}()

	logger.Info("запуск smsgateway",
		"version", version,
		"build_time", buildTime,
		"http_addr", cfg.HTTPAddr,
		"serial_port", cfg.SerialPort,
		"modem_profile", cfg.ModemProfile,
		"db_path", cfg.DBPath,
	)

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- Инициализация БД ---
	if err := ensureDir(cfg.DBPath); err != nil {
		notifier.Notify(ctx, notify.LevelCritical, err)
		return err
	}
	store, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		err = fmt.Errorf("открытие БД: %w", err)
		notifier.Notify(ctx, notify.LevelCritical, err)
		return err
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			logger.Error("закрытие БД", "err", cerr)
		}
	}()
	logger.Info("БД готова", "db_path", cfg.DBPath)

	retentionPolicy := storage.RetentionPolicy{
		Messages: cfg.SMSRetention,
	}
	go store.RunRetention(ctx, retentionPolicy, logger)

	// --- Модем или режим без модема ---
	var (
		serviceModem service.Modem // для service
		realModem    *modem.Modem  // nil в режиме без модема
	)
	if cfg.SerialPort == "" || cfg.SerialPort == "none" {
		logger.Warn("модем не сконфигурирован (SERIAL_PORT пуст) — режим без модема")
		serviceModem = nopModem{}
	} else {
		profile, err := profiles.Get(cfg.ModemProfile)
		if err != nil {
			err = fmt.Errorf("профиль модема: %w", err)
			notifier.Notify(ctx, notify.LevelCritical, err)
			return err
		}
		logger.Info("профиль модема", "profile", profile.Name())

		port, err := modem.OpenSerial(modem.SerialConfig{
			Port: cfg.SerialPort,
			Baud: cfg.SerialBaud,
		})
		if err != nil {
			err = fmt.Errorf("открытие serial: %w", err)
			notifier.Notify(ctx, notify.LevelCritical, err)
			return err
		}

		m, err := modem.NewModem(port, profile,
			cfg.ModemCmdTimeout, slogAdapter{logger})
		if err != nil {
			_ = port.Close()
			err = fmt.Errorf("создание Modem: %w", err)
			notifier.Notify(ctx, notify.LevelCritical, err)
			return err
		}
		defer func() {
			if cerr := m.Close(); cerr != nil {
				logger.Error("закрытие модема", "err", cerr)
			}
		}()

		if cfg.ModemPIN != "" {
			m.SetPIN(cfg.ModemPIN)
		}

		serviceModem = m
		realModem = m
	}

	// --- Gammu Manager (опционально) ---
	var gammuMgr *gammu.Manager
	if cfg.GammuEnabled() {
		inbox, err := gammu.NewInboxWriter(
			cfg.GammuInboxPath(), cfg.GammuInboxFormat, logger)
		if err != nil {
			err = fmt.Errorf("gammu inbox: %w", err)
			notifier.Notify(ctx, notify.LevelCritical, err)
			return err
		}

		var sender gammu.Sender
		if realModem != nil {
			sender = service.NewModemSender(realModem, logger)
		}
		outboxStore := service.NewStorageOutboxStore(store)

		gammuMgr, err = gammu.NewManager(gammu.ManagerConfig{
			InboxDir:      cfg.GammuInboxPath(),
			OutboxDir:     cfg.GammuOutboxPath(),
			SentDir:       cfg.GammuSentPath(),
			ErrorDir:      cfg.GammuErrorPath(),
			InboxFormat:   cfg.GammuInboxFormat,
			WatchInterval: cfg.GammuWatchInterval,
			MaxAttempts:   cfg.GammuOutboxMaxAttempts,
		}, sender, outboxStore, inbox, logger)
		if err != nil {
			err = fmt.Errorf("gammu manager: %w", err)
			notifier.Notify(ctx, notify.LevelCritical, err)
			return err
		}

		logger.Info("gammu: файловый бэкенд активен",
			"root", cfg.GammuRoot,
			"inbox", cfg.GammuInboxPath(),
			"outbox", cfg.GammuOutboxPath(),
			"sent", cfg.GammuSentPath(),
			"error", cfg.GammuErrorPath(),
			"format", cfg.GammuInboxFormat,
			"watch_interval", cfg.GammuWatchInterval,
			"max_attempts", cfg.GammuOutboxMaxAttempts,
			"sender", realModem != nil,
		)
	}

	// --- Сервис ---
	svc := service.New(serviceModem, store, logger, service.Config{
		DeleteAfterRead: cfg.SMSDeleteAfterRead,
		SelfNumber:      cfg.SelfNumber,
		TestSMSTimeout:  cfg.TestSMSTimeout,
		Notifier:        notifier,
		Gammu:           gammuMgr,
	})

	svcErr := make(chan error, 1)
	go func() {
		svcErr <- svc.Run(ctx)
	}()

	// --- HTTP API ---
	apiServer := api.New(cfg, store, svc, notifier, logger)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- apiServer.Start()
	}()

	select {
	case <-ctx.Done():
		logger.Info("получен сигнал остановки")
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second)
		defer cancel()
		if err := apiServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown api", "err", err)
		}
	case err := <-serverErr:
		if err != nil {
			notifier.Notify(ctx, notify.LevelCritical, err)
			return fmt.Errorf("http server: %w", err)
		}
	case err := <-svcErr:
		if err != nil {
			return fmt.Errorf("service: %w", err)
		}
	}

	logger.Info("остановлен")
	return nil
}

func ensureDir(dbPath string) error {
	if dbPath == ":memory:" || dbPath == "" {
		return nil
	}
	dir := filepath.Dir(dbPath)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("создание каталога %q: %w", dir, err)
	}
	return nil
}

// nopModem — заглушка для режима "без модема".
type nopModem struct{}

func (nopModem) Init(context.Context) error { return nil }
func (nopModem) SendSMS(context.Context, string, string, modem.SendOptions) (modem.SendResult, error) {
	return modem.SendResult{}, fmt.Errorf("модем не сконфигурирован")
}
func (nopModem) ReadSMS(context.Context, modem.ReadFilter) ([]modem.SMSMessage, error) {
	return nil, nil
}
func (nopModem) DeleteSMS(context.Context, int) error { return nil }
func (nopModem) Exec(context.Context, string) (string, error) {
	return "", fmt.Errorf("модем не сконфигурирован")
}
func (nopModem) URCs() <-chan modem.URC { return make(chan modem.URC) }
func (nopModem) Info() modem.Info       { return modem.Info{} }
func (nopModem) Close() error           { return nil }

type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) Debug(msg string, args ...any) { a.l.Debug(msg, args...) }
func (a slogAdapter) Info(msg string, args ...any)  { a.l.Info(msg, args...) }
func (a slogAdapter) Warn(msg string, args ...any)  { a.l.Warn(msg, args...) }
func (a slogAdapter) Error(msg string, args ...any) { a.l.Error(msg, args...) }

var _ = io.Discard

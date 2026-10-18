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
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
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
	"github.com/ik-vidya/smsgateway/internal/webui"
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
		cmdDetectModem(),
		cmdProbe(),
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

	logBuffer := logging.NewBuffer(1000)

	logger, err := logging.NewWithBuffer(cfg.LogLevel, cfg.LogFormat, logBuffer)
	if err != nil {
		return err
	}

	// Ресурсы, которые надо закрыть при выходе и при reload.
	// cleanupOnce гарантирует, что они закроются ровно один раз,
	// независимо от того, штатный это выход или syscall.Exec.
	var (
		cleanupOnce sync.Once
		notifier    notify.Notifier
		store       *storage.Store
		realModem   *modem.Modem
	)
	cleanup := func() {
		cleanupOnce.Do(func() {
			if notifier != nil {
				if cerr := notifier.Close(); cerr != nil {
					logger.Error("закрытие notifier", "err", cerr)
				}
			}
			if store != nil {
				if cerr := store.Close(); cerr != nil {
					logger.Error("закрытие БД", "err", cerr)
				}
			}
			if realModem != nil {
				if cerr := realModem.Close(); cerr != nil {
					logger.Error("закрытие модема", "err", cerr)
				}
			}
		})
	}
	defer cleanup()

	// Notifier (Telegram или Noop).
	notifier = notify.Noop{}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		tg, err := notify.NewTelegram(notify.TelegramConfig{
			BotToken: cfg.TelegramBotToken,
			ChatID:   cfg.TelegramChatID,
			Logger:   logger,
		})
		if err != nil {
			logger.Error("не удалось создать Telegram-нотификатор",
				"err", err)
			notifier = notify.Noop{}
		} else {
			notifier = tg
			logger.Info("Telegram-нотификатор активен")
		}
	}

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

	// SIGHUP → перечитать .env и перезапустить процесс через exec.
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)
	defer signal.Stop(reloadCh)

	// БД.
	if err := ensureDir(cfg.DBPath); err != nil {
		notifier.Notify(ctx, notify.LevelCritical, err)
		return err
	}
	store, err = storage.Open(ctx, cfg.DBPath)
	if err != nil {
		err = fmt.Errorf("открытие БД: %w", err)
		notifier.Notify(ctx, notify.LevelCritical, err)
		return err
	}
	logger.Info("БД готова", "db_path", cfg.DBPath)

	retentionPolicy := storage.RetentionPolicy{
		Messages: cfg.SMSRetention,
	}
	go store.RunRetention(ctx, retentionPolicy, logger)

	// Модем или режим без модема.
	var serviceModem service.Modem
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

		m, err := modem.NewModemWithCMGS(port, profile,
			cfg.ModemCmdTimeout, cfg.ModemCMGSTimeout,
			slogAdapter{logger})
		if err != nil {
			_ = port.Close()
			err = fmt.Errorf("создание Modem: %w", err)
			notifier.Notify(ctx, notify.LevelCritical, err)
			return err
		}
		realModem = m

		if cfg.ModemPIN != "" {
			m.SetPIN(cfg.ModemPIN)
		}

		serviceModem = m
	}

	// Gammu Manager (опционально).
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

	// Сервис.
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

	// HTTP API.
	apiServer := api.New(cfg, store, svc, notifier, logger,
		version, buildTime)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- apiServer.Start()
	}()

	// Web UI (опционально).
	var webServer *webui.Server
	webErr := make(chan error, 1)
	if cfg.WebUIEnabled {
		ws, err := webui.New(cfg, store, svc, logBuffer, logger,
			version, flagConfig)
		if err != nil {
			err = fmt.Errorf("web ui: %w", err)
			notifier.Notify(ctx, notify.LevelCritical, err)
			return err
		}
		webServer = ws
		logger.Info("Web UI запущен", "addr", cfg.WebUIAddr)
		go func() {
			webErr <- webServer.Start()
		}()
	}

	select {
	case <-ctx.Done():
		logger.Info("получен сигнал остановки")
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second)
		defer cancel()
		if err := apiServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown api", "err", err)
		}
		if webServer != nil {
			if err := webServer.Shutdown(shutdownCtx); err != nil {
				logger.Error("shutdown web ui", "err", err)
			}
		}
	case <-reloadCh:
		logger.Info("SIGHUP: перезапуск через exec")
		cancel() // остановить горутины
		// Дождаться сервиса — он закроет модем, gammu, urcLoop и т.п.
		select {
		case err := <-svcErr:
			if err != nil {
				logger.Warn("service завершился с ошибкой",
					"err", err)
			}
		case <-time.After(15 * time.Second):
			logger.Warn("таймаут остановки service")
		}
		// Остановить API и Web UI, чтобы освободить порты.
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(), 10*time.Second)
		if err := apiServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown api", "err", err)
		}
		if webServer != nil {
			if err := webServer.Shutdown(shutdownCtx); err != nil {
				logger.Error("shutdown web ui", "err", err)
			}
		}
		shutdownCancel()

		// Закрыть ресурсы до exec (defer'ы не сработают).
		cleanup()

		// Перечитываем .env в os.Environ() ПЕРЕД exec. Иначе
		// syscall.Exec унаследует os.Environ() родителя, где
		// SERIAL_PORT и другие — старые значения из первого
		// запуска. godotenv.Load в новом процессе не перезапишет
		// их (Load не override), и hot-reload «молча» не
		// применит изменения из .env.
		//
		// Overload здесь — сознательно: пользователь нажал
		// «Сохранить» в UI, .env должен стать источником истины.
		// Обычный startup по-прежнему использует Load: env
		// побеждает .env (systemd EnvironmentFile и т.п.).
		if flagConfig != "" {
			if err := godotenv.Overload(flagConfig); err != nil {
				logger.Warn("SIGHUP: godotenv.Overload", "err", err)
			}
		}

		exe, err := os.Executable()
		if err != nil {
			exe = "/proc/self/exe"
		}
		logger.Info("exec", "exe", exe, "args", os.Args)
		if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
			logger.Error("syscall.Exec", "err", err)
			return fmt.Errorf("syscall.Exec: %w", err)
		}
		// Сюда не попадём.
		return nil
	case err := <-serverErr:
		if err != nil {
			notifier.Notify(ctx, notify.LevelCritical, err)
			return fmt.Errorf("http server: %w", err)
		}
	case err := <-webErr:
		if err != nil {
			notifier.Notify(ctx, notify.LevelCritical, err)
			return fmt.Errorf("web ui: %w", err)
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

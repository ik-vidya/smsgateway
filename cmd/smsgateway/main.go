// Команда smsgateway — SMS-шлюз для AT-совместимых GSM-модемов.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ik-vidya/smsgateway/internal/api"
	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/logging"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

// Версия и время сборки подставляются через -ldflags.
var (
	version   = "dev"
	buildTime = "unknown"
)

// Глобальные флаги (persistent).
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

// rootCmd — корневая команда. Без подкоманды запускает сервис.
var rootCmd = &cobra.Command{
	Use:   "smsgateway",
	Short: "SMS-шлюз для AT-совместимых GSM-модемов",
	Long: `smsgateway — долгоживущий сервис приёма и отправки SMS через
AT-совместимый GSM-модем. Предоставляет HTTP/JSON REST API, сохраняет
сообщения в SQLite, работает с Gammu file backend.

Без подкоманды запускает сервис. Требуется флаг --config.

Примеры:
    smsgateway --config /etc/smsgateway/.env
    smsgateway version
    smsgateway migrate --config /etc/smsgateway/.env`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runService,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "",
		"путь к .env-файлу (обязателен для всех команд, кроме version)")
	rootCmd.Flags().BoolVarP(&flagShowVersion, "version", "v", false,
		"показать версию и выйти")

	rootCmd.AddCommand(
		cmdVersion(),
		cmdMigrate(),
	)
}

// cmdVersion — подкоманда `smsgateway version`.
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

// cmdMigrate — подкоманда `smsgateway migrate`.
func cmdMigrate() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Применить миграции БД и выйти",
		Long: `Открывает базу данных из конфига, применяет неприменённые
миграции и завершается. Идемпотентна: повторный запуск безопасен.`,
		RunE: runMigrate,
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

// runService запускает основной сервис.
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
	logger.Info("БД готова", "db_path", cfg.DBPath)

	// --- Retention в фоне ---
	retentionPolicy := storage.RetentionPolicy{
		Messages: cfg.SMSRetention,
	}
	go store.RunRetention(ctx, retentionPolicy, logger)

	// --- HTTP API ---
	apiServer := api.New(cfg, store, logger)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- apiServer.Start()
	}()

	// --- Ожидание сигнала остановки или падения сервера ---
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
			return fmt.Errorf("http server: %w", err)
		}
	}

	logger.Info("остановлен")
	return nil
}

// ensureDir создаёт родительский каталог для файла БД, если его нет.
// Для ":memory:" ничего не делает.
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
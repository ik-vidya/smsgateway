// Команда smsgateway — SMS-шлюз для AT-совместимых GSM-модемов.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/logging"
)

// Версия подставляется через -ldflags.
var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	configPath := flag.String("config", "", "путь к .env-файлу (обязательно)")
	showVersion := flag.Bool("version", false, "показать версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Printf("smsgateway %s (сборка %s)\n", version, buildTime)
		return
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
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

	// TODO(итерация 2+): storage, modem, service, api, webui, notify.
	logger.Info("инициализация (заглушка), ожидаю сигнала остановки")

	<-ctx.Done()
	logger.Info("получен сигнал остановки, выхожу")
	return nil
}

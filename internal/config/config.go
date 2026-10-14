// Package config — загрузка и валидация конфигурации из .env.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config — вся конфигурация приложения.
type Config struct {
	// HTTP API
	HTTPAddr string
	APIKeys  []string

	// Modem
	SerialPort         string
	SerialBaud         int
	ModemCmdTimeout    time.Duration
	ModemProfile       string
	ModemStorage       string
	SMSDeleteAfterRead bool

	// Storage
	DBPath       string
	SMSRetention time.Duration

	// Gammu file backend
	FilesBackendDir     string
	GammuImportDir      string
	GammuImportInterval time.Duration

	// Logging
	LogLevel  string
	LogFormat string

	// Self-test
	SelfNumber     string
	TestSMSTimeout time.Duration

	// Telegram
	TelegramBotToken string
	TelegramChatID   string

	// Web UI
	WebUIEnabled     bool
	WebUIAddr        string
	WebUIAuthMode    string
	WebUIDefaultLang string
	WebUISupported   []string
}

// Load читает .env по указанному пути и валидирует результат.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("путь к конфигу обязателен (флаг -config)")
	}
	if err := godotenv.Load(path); err != nil {
		return nil, fmt.Errorf("загрузка конфига %q: %w", path, err)
	}

	c := &Config{
		HTTPAddr:           envDefault("HTTP_ADDR", "127.0.0.1:8080"),
		SerialPort:         os.Getenv("SERIAL_PORT"),
		ModemProfile:       envDefault("MODEM_PROFILE", "generic"),
		ModemStorage:       os.Getenv("MODEM_STORAGE"),
		DBPath:             os.Getenv("DB_PATH"),
		FilesBackendDir:    os.Getenv("FILES_BACKEND_DIR"),
		GammuImportDir:     os.Getenv("GAMMU_IMPORT_DIR"),
		LogLevel:           envDefault("LOG_LEVEL", "info"),
		LogFormat:          envDefault("LOG_FORMAT", "text"),
		SelfNumber:         os.Getenv("SELF_NUMBER"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:     os.Getenv("TELEGRAM_CHAT_ID"),
		WebUIAddr:          envDefault("WEBUI_ADDR", "127.0.0.1:8081"),
		WebUIAuthMode:      envDefault("WEBUI_AUTH_MODE", "none"),
		WebUIDefaultLang:   envDefault("WEBUI_DEFAULT_LANG", "en"),
	}

	c.APIKeys = splitNonEmpty(os.Getenv("API_KEYS"), ",")
	if len(c.APIKeys) == 0 {
		if k := os.Getenv("API_KEY"); k != "" {
			c.APIKeys = []string{k}
		}
	}

	var err error
	if c.SerialBaud, err = envInt("SERIAL_BAUD", 115200); err != nil {
		return nil, err
	}
	if c.ModemCmdTimeout, err = envDuration("MODEM_CMD_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}
	if c.SMSRetention, err = envDuration("SMS_RETENTION", 0); err != nil {
		return nil, err
	}
	if c.GammuImportInterval, err = envDuration("GAMMU_IMPORT_INTERVAL", 0); err != nil {
		return nil, err
	}
	if c.TestSMSTimeout, err = envDuration("TEST_SMS_TIMEOUT", 30*time.Second); err != nil {
		return nil, err
	}

	c.SMSDeleteAfterRead = envBool("SMS_DELETE_AFTER_READ", true)
	c.WebUIEnabled = envBool("WEBUI_ENABLED", false)
	c.WebUISupported = splitNonEmpty(
		envDefault("WEBUI_SUPPORTED_LANGS", "ru,lv,en"), ",")

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	var errs []error
	if c.SerialPort == "" {
		errs = append(errs, errors.New("SERIAL_PORT обязателен"))
	}
	if c.DBPath == "" {
		errs = append(errs, errors.New("DB_PATH обязателен"))
	}
	if len(c.APIKeys) == 0 {
		errs = append(errs, errors.New("нужен API_KEY или API_KEYS"))
	}
	switch strings.ToLower(c.ModemProfile) {
	case "sim800", "quectel", "telit", "generic":
	default:
		errs = append(errs, fmt.Errorf(
			"MODEM_PROFILE должен быть sim800|quectel|telit|generic, получено %q",
			c.ModemProfile))
	}
	switch strings.ToLower(c.ModemStorage) {
	case "", "me", "sm":
	default:
		errs = append(errs, fmt.Errorf(
			"MODEM_STORAGE должен быть ME, SM или пуст, получено %q",
			c.ModemStorage))
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		errs = append(errs, fmt.Errorf(
			"LOG_LEVEL должен быть debug|info|warn|error, получено %q", c.LogLevel))
	}
	switch strings.ToLower(c.LogFormat) {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf(
			"LOG_FORMAT должен быть text|json, получено %q", c.LogFormat))
	}
	return errors.Join(errs...)
}

// ==================== env helpers ====================

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q не число: %w", key, v, err)
	}
	return n, nil
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	return d, nil
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseDuration поддерживает суффиксы h, m, s, ms и d (дни),
// а также комбинированные значения вида "1d12h".
func ParseDuration(s string) (time.Duration, error) {
	var total time.Duration
	rest := strings.TrimSpace(s)
	for rest != "" {
		i := 0
		for i < len(rest) && (rest[i] >= '0' && rest[i] <= '9' || rest[i] == '.') {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("некорректный duration %q", s)
		}
		numStr := rest[:i]
		unit := ""
		for i < len(rest) && !(rest[i] >= '0' && rest[i] <= '9') && rest[i] != '.' {
			unit += string(rest[i])
			i++
		}
		rest = rest[i:]

		switch unit {
		case "d":
			n, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, err
			}
			total += time.Duration(n * float64(24*time.Hour))
		case "h", "m", "s", "ms", "us", "ns", "":
			d, err := time.ParseDuration(numStr + unit)
			if err != nil {
				return 0, err
			}
			total += d
		default:
			return 0, fmt.Errorf("неизвестная единица %q в %q", unit, s)
		}
	}
	return total, nil
}

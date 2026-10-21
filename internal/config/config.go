// Package config — загрузка и валидация конфигурации из .env.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	ModemDescription   string
	SerialPort         string
	SerialBaud         int
	ModemCmdTimeout    time.Duration
	ModemCMGSTimeout   time.Duration
	ModemProfile       string
	ModemStorage       string
	ModemPIN           string
	SMSDeleteAfterRead bool

	// Storage
	DBPath       string
	SMSRetention time.Duration

	// Gammu file backend
	//
	// Активируется, если GammuRoot не пуст. Inbox/Outbox/Sent/Error —
	// подкаталоги, относительные пути склеиваются с GammuRoot.
	// Абсолютные пути используются как есть.
	GammuRoot              string
	GammuInbox             string
	GammuOutbox            string
	GammuSent              string
	GammuError             string
	GammuInboxFormat       string        // standard | unicode | utf8 | detail
	GammuWatchInterval     time.Duration // период сканирования outbox
	GammuOutboxMaxAttempts int           // всего попыток отправки, включая первую

	// Logging
	LogLevel  string
	LogFormat string

	// Self-test
	SelfNumber     string
	TestSMSTimeout time.Duration

	// Telegram
	TelegramBotToken string
	TelegramChatID   string
	TelegramLang     string

	// Web UI
	WebUIEnabled     bool
	WebUIAddr        string
	WebUIAuthMode    string
	WebUIAuthUser    string
	WebUIAuthPass    string
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
		HTTPAddr:         envDefault("HTTP_ADDR", "127.0.0.1:8080"),
		ModemDescription: os.Getenv("MODEM_DESCRIPTION"),
		SerialPort:       os.Getenv("SERIAL_PORT"),
		ModemProfile:     envDefault("MODEM_PROFILE", "generic"),
		ModemStorage:     os.Getenv("MODEM_STORAGE"),
		ModemPIN:         os.Getenv("MODEM_PIN"),
		DBPath:           os.Getenv("DB_PATH"),
		GammuRoot:        os.Getenv("GAMMU_ROOT"),
		GammuInbox:       envDefault("GAMMU_INBOX", "inbox"),
		GammuOutbox:      envDefault("GAMMU_OUTBOX", "outbox"),
		GammuSent:        envDefault("GAMMU_SENT", "sent"),
		GammuError:       envDefault("GAMMU_ERROR", "error"),
		GammuInboxFormat: envDefault("GAMMU_INBOX_FORMAT", "utf8"),
		LogLevel:         envDefault("LOG_LEVEL", "info"),
		LogFormat:        envDefault("LOG_FORMAT", "text"),
		SelfNumber:       os.Getenv("SELF_NUMBER"),
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),
		TelegramLang:     envDefault("TELEGRAM_LANG", "ru"),
		WebUIAddr:        envDefault("WEBUI_ADDR", "127.0.0.1:8081"),
		WebUIAuthMode:    envDefault("WEBUI_AUTH_MODE", "none"),
		WebUIAuthUser:    os.Getenv("WEBUI_AUTH_USER"),
		WebUIAuthPass:    os.Getenv("WEBUI_AUTH_PASS"),
		WebUIDefaultLang: envDefault("WEBUI_DEFAULT_LANG", "en"),
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
	if c.ModemCMGSTimeout, err = envDuration("MODEM_CMGS_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if c.SMSRetention, err = envDuration("SMS_RETENTION", 0); err != nil {
		return nil, err
	}
	if c.TestSMSTimeout, err = envDuration("TEST_SMS_TIMEOUT", 120*time.Second); err != nil {
		return nil, err
	}
	if c.GammuWatchInterval, err = envDuration("GAMMU_WATCH_INTERVAL", 5*time.Second); err != nil {
		return nil, err
	}
	if c.GammuOutboxMaxAttempts, err = envInt("GAMMU_OUTBOX_MAX_ATTEMPTS", 5); err != nil {
		return nil, err
	}

	c.SMSDeleteAfterRead = envBool("SMS_DELETE_AFTER_READ", true)
	c.WebUIEnabled = envBool("WEBUI_ENABLED", false)
	c.WebUISupported = splitNonEmpty(
		envDefault("WEBUI_SUPPORTED_LANGS", "ru,lv,en"), ",")

	if err := c.validate(); err != nil {
		return nil, err
	}

	// Автогенерация MODEM_DESCRIPTION, если не задана
	if c.ModemDescription == "" && c.SerialPort != "" && c.SerialPort != "none" {
		hostname, _ := os.Hostname()
		base := filepath.Base(c.SerialPort)
		// Если это длинный by-id, берем только хвост
		if len(base) > 20 {
			base = "modem"
		}
		c.ModemDescription = fmt.Sprintf("%s-%s", hostname, base)
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
	case "sim800", "quectel", "telit", "huawei", "generic":
	default:
		errs = append(errs, fmt.Errorf(
			"MODEM_PROFILE должен быть "+
				"sim800|quectel|telit|huawei|generic, получено %q",
			c.ModemProfile))
	}
	switch strings.ToLower(c.ModemStorage) {
	case "", "me", "sm":
	default:
		errs = append(errs, fmt.Errorf(
			"MODEM_STORAGE должен быть ME, SM или пуст, получено %q",
			c.ModemStorage))
	}
	if c.ModemPIN != "" {
		if len(c.ModemPIN) < 4 || len(c.ModemPIN) > 8 {
			errs = append(errs, fmt.Errorf(
				"MODEM_PIN должен быть 4-8 цифр, получено %d",
				len(c.ModemPIN)))
		}
		for _, r := range c.ModemPIN {
			if r < '0' || r > '9' {
				errs = append(errs, errors.New(
					"MODEM_PIN должен содержать только цифры"))
				break
			}
		}
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
	// Telegram: либо оба параметра, либо ни одного.
	hasToken := strings.TrimSpace(c.TelegramBotToken) != ""
	hasChat := strings.TrimSpace(c.TelegramChatID) != ""
	switch strings.ToLower(c.TelegramLang) {
	case "ru", "lv", "en":
	default:
		errs = append(errs, fmt.Errorf(
			"TELEGRAM_LANG должен быть ru|lv|en, получено %q", c.TelegramLang))
	}

	if hasToken != hasChat {
		errs = append(errs, errors.New(
			"TELEGRAM_BOT_TOKEN и TELEGRAM_CHAT_ID должны быть заданы вместе"))
	}

	// Gammu file backend.
	if c.GammuRoot != "" {
		switch strings.ToLower(c.GammuInboxFormat) {
		case "standard", "unicode", "utf8", "detail":
		default:
			errs = append(errs, fmt.Errorf(
				"GAMMU_INBOX_FORMAT должен быть standard|unicode|utf8|detail, получено %q",
				c.GammuInboxFormat))
		}
		if c.GammuOutboxMaxAttempts < 1 {
			errs = append(errs, fmt.Errorf(
				"GAMMU_OUTBOX_MAX_ATTEMPTS должен быть >= 1, получено %d",
				c.GammuOutboxMaxAttempts))
		}
		if c.GammuInbox == "" || c.GammuOutbox == "" ||
			c.GammuSent == "" || c.GammuError == "" {
			errs = append(errs, errors.New(
				"при заданном GAMMU_ROOT все подкаталоги "+
					"(GAMMU_INBOX, GAMMU_OUTBOX, GAMMU_SENT, GAMMU_ERROR) "+
					"должны быть непустыми"))
		}
	}

	return errors.Join(errs...)
}

// GammuEnabled сообщает, сконфигурирован ли Gammu file backend.
func (c *Config) GammuEnabled() bool { return c.GammuRoot != "" }

// GammuInboxPath возвращает абсолютный путь к inbox-директории.
func (c *Config) GammuInboxPath() string { return c.gammuPath(c.GammuInbox) }

// GammuOutboxPath возвращает абсолютный путь к outbox-директории.
func (c *Config) GammuOutboxPath() string { return c.gammuPath(c.GammuOutbox) }

// GammuSentPath возвращает абсолютный путь к sent-директории.
func (c *Config) GammuSentPath() string { return c.gammuPath(c.GammuSent) }

// GammuErrorPath возвращает абсолютный путь к error-директории.
func (c *Config) GammuErrorPath() string { return c.gammuPath(c.GammuError) }

// gammuPath склеивает GammuRoot с подкаталогом. Абсолютный
// подкаталог возвращается как есть.
func (c *Config) gammuPath(sub string) string {
	if c.GammuRoot == "" || sub == "" {
		return ""
	}
	if filepath.IsAbs(sub) {
		return sub
	}
	return filepath.Join(c.GammuRoot, sub)
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

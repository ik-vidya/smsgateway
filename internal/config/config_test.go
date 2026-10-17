package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"720h":  720 * time.Hour,
		"7d":    7 * 24 * time.Hour,
		"1d12h": 36 * time.Hour,
		"30m":   30 * time.Minute,
		"30s":   30 * time.Second,
		"1d":    24 * time.Hour,
		"":      0,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
	if _, err := ParseDuration("garbage"); err == nil {
		t.Error("ожидалась ошибка для garbage")
	}
}

// clearGammuEnv сбрасывает переменные окружения после Load:
// godotenv пишет напрямую в os.Environ, а не через t.Setenv.
func clearGammuEnv(keys ...string) {
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func TestLoadGammuDefaults(t *testing.T) {
	defer clearGammuEnv(
		"SERIAL_PORT", "DB_PATH", "API_KEY",
		"GAMMU_ROOT", "GAMMU_INBOX", "GAMMU_OUTBOX",
		"GAMMU_SENT", "GAMMU_ERROR", "GAMMU_INBOX_FORMAT",
		"GAMMU_WATCH_INTERVAL", "GAMMU_OUTBOX_MAX_ATTEMPTS",
	)

	env := "SERIAL_PORT=none\n" +
		"DB_PATH=:memory:\n" +
		"API_KEY=test\n" +
		"GAMMU_ROOT=/tmp/gammu\n"
	cfg, err := Load(writeEnv(t, env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.GammuRoot != "/tmp/gammu" {
		t.Errorf("GammuRoot=%q", cfg.GammuRoot)
	}
	if cfg.GammuInbox != "inbox" {
		t.Errorf("GammuInbox=%q", cfg.GammuInbox)
	}
	if cfg.GammuOutbox != "outbox" {
		t.Errorf("GammuOutbox=%q", cfg.GammuOutbox)
	}
	if cfg.GammuSent != "sent" {
		t.Errorf("GammuSent=%q", cfg.GammuSent)
	}
	if cfg.GammuError != "error" {
		t.Errorf("GammuError=%q", cfg.GammuError)
	}
	if cfg.GammuInboxFormat != "utf8" {
		t.Errorf("GammuInboxFormat=%q", cfg.GammuInboxFormat)
	}
	if cfg.GammuWatchInterval != 5*time.Second {
		t.Errorf("GammuWatchInterval=%v", cfg.GammuWatchInterval)
	}
	if cfg.GammuOutboxMaxAttempts != 5 {
		t.Errorf("GammuOutboxMaxAttempts=%d", cfg.GammuOutboxMaxAttempts)
	}
	if !cfg.GammuEnabled() {
		t.Error("GammuEnabled()=false, ожидалось true")
	}
}

func TestLoadGammuCustomPaths(t *testing.T) {
	defer clearGammuEnv(
		"SERIAL_PORT", "DB_PATH", "API_KEY",
		"GAMMU_ROOT", "GAMMU_OUTBOX", "GAMMU_WATCH_INTERVAL",
		"GAMMU_OUTBOX_MAX_ATTEMPTS",
	)

	env := "SERIAL_PORT=none\n" +
		"DB_PATH=:memory:\n" +
		"API_KEY=test\n" +
		"GAMMU_ROOT=/var/spool/sms\n" +
		"GAMMU_OUTBOX=queue\n" +
		"GAMMU_WATCH_INTERVAL=2s\n" +
		"GAMMU_OUTBOX_MAX_ATTEMPTS=3\n"
	cfg, err := Load(writeEnv(t, env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.GammuOutboxPath() != "/var/spool/sms/queue" {
		t.Errorf("GammuOutboxPath=%q", cfg.GammuOutboxPath())
	}
	if cfg.GammuWatchInterval != 2*time.Second {
		t.Errorf("GammuWatchInterval=%v", cfg.GammuWatchInterval)
	}
	if cfg.GammuOutboxMaxAttempts != 3 {
		t.Errorf("GammuOutboxMaxAttempts=%d", cfg.GammuOutboxMaxAttempts)
	}
}

func TestLoadGammuAbsoluteSubdir(t *testing.T) {
	defer clearGammuEnv(
		"SERIAL_PORT", "DB_PATH", "API_KEY",
		"GAMMU_ROOT", "GAMMU_OUTBOX",
	)

	env := "SERIAL_PORT=none\n" +
		"DB_PATH=:memory:\n" +
		"API_KEY=test\n" +
		"GAMMU_ROOT=/var/spool/sms\n" +
		"GAMMU_OUTBOX=/some/absolute/outbox\n"
	cfg, err := Load(writeEnv(t, env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.GammuOutboxPath() != "/some/absolute/outbox" {
		t.Errorf("GammuOutboxPath=%q", cfg.GammuOutboxPath())
	}
}

func TestLoadGammuDisabled(t *testing.T) {
	defer clearGammuEnv(
		"SERIAL_PORT", "DB_PATH", "API_KEY", "GAMMU_ROOT",
	)

	env := "SERIAL_PORT=none\n" +
		"DB_PATH=:memory:\n" +
		"API_KEY=test\n"
	cfg, err := Load(writeEnv(t, env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GammuEnabled() {
		t.Error("GammuEnabled()=true, ожидалось false")
	}
	if cfg.GammuOutboxPath() != "" {
		t.Errorf("GammuOutboxPath=%q, ожидалось пусто", cfg.GammuOutboxPath())
	}
}

func TestLoadGammuBadInboxFormat(t *testing.T) {
	defer clearGammuEnv(
		"SERIAL_PORT", "DB_PATH", "API_KEY",
		"GAMMU_ROOT", "GAMMU_INBOX_FORMAT",
	)

	env := "SERIAL_PORT=none\n" +
		"DB_PATH=:memory:\n" +
		"API_KEY=test\n" +
		"GAMMU_ROOT=/tmp/gammu\n" +
		"GAMMU_INBOX_FORMAT=weird\n"
	if _, err := Load(writeEnv(t, env)); err == nil {
		t.Fatal("ожидалась ошибка для неверного GAMMU_INBOX_FORMAT")
	}
}

func TestLoadGammuBadMaxAttempts(t *testing.T) {
	defer clearGammuEnv(
		"SERIAL_PORT", "DB_PATH", "API_KEY",
		"GAMMU_ROOT", "GAMMU_OUTBOX_MAX_ATTEMPTS",
	)

	env := "SERIAL_PORT=none\n" +
		"DB_PATH=:memory:\n" +
		"API_KEY=test\n" +
		"GAMMU_ROOT=/tmp/gammu\n" +
		"GAMMU_OUTBOX_MAX_ATTEMPTS=0\n"
	if _, err := Load(writeEnv(t, env)); err == nil {
		t.Fatal("ожидалась ошибка для GAMMU_OUTBOX_MAX_ATTEMPTS=0")
	}
}

// ==================== modem profile validation ====================

func TestModemProfileValidation(t *testing.T) {
	cases := []struct {
		profile string
		ok      bool
	}{
		{"sim800", true},
		{"SIM800", true},
		{"quectel", true},
		{"telit", true},
		{"huawei", true},
		{"Huawei", true},
		{"generic", true},
		{"", true}, // пусто = generic по умолчанию
		{"nokia", false},
		{"sim900", false},
	}
	for _, c := range cases {
		t.Run(c.profile, func(t *testing.T) {
			clearEnv(t, "SERIAL_PORT", "DB_PATH", "API_KEY",
				"MODEM_PROFILE")
			env := "SERIAL_PORT=none\n" +
				"DB_PATH=:memory:\n" +
				"API_KEY=test\n"
			if c.profile != "" {
				env += "MODEM_PROFILE=" + c.profile + "\n"
			}
			_, err := Load(writeEnv(t, env))
			if c.ok && err != nil {
				t.Errorf("профиль %q: неожиданная ошибка: %v",
					c.profile, err)
			}
			if !c.ok && err == nil {
				t.Errorf("профиль %q: ожидалась ошибка", c.profile)
			}
		})
	}
}

// clearEnv — сброс переменных окружения между тестами.
// (в файле уже есть clearGammuEnv, но она с фиксированным списком)
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}

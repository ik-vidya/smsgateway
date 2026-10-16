package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func readEnvFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(b)
}

func TestUpdateEnvFileReplaceExisting(t *testing.T) {
	p := writeEnvFile(t, `# smsgateway config
SERIAL_PORT=/dev/ttyUSB0
DB_PATH=./data/sms.db
LOG_LEVEL=info
`)
	applied, err := UpdateEnvFile(p, map[string]string{
		"SERIAL_PORT": "/dev/serial/by-id/usb-test",
	})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	if len(applied) != 1 || applied[0] != "SERIAL_PORT=/dev/serial/by-id/usb-test" {
		t.Errorf("applied=%v", applied)
	}

	got := readEnvFile(t, p)
	if !strings.Contains(got, "SERIAL_PORT=/dev/serial/by-id/usb-test") {
		t.Errorf("SERIAL_PORT не заменён:\n%s", got)
	}
	// Комментарий и остальные переменные не пострадали.
	for _, want := range []string{
		"# smsgateway config",
		"DB_PATH=./data/sms.db",
		"LOG_LEVEL=info",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("потеряна строка %q:\n%s", want, got)
		}
	}
}

func TestUpdateEnvFileNoChange(t *testing.T) {
	p := writeEnvFile(t, "SERIAL_PORT=/dev/ttyUSB0\n")
	before := readEnvFile(t, p)

	applied, err := UpdateEnvFile(p, map[string]string{
		"SERIAL_PORT": "/dev/ttyUSB0",
	})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("applied=%v, ожидалось пусто (идемпотентно)", applied)
	}
	if readEnvFile(t, p) != before {
		t.Error("файл изменён, хотя значение то же")
	}
}

func TestUpdateEnvFileQuotedValue(t *testing.T) {
	p := writeEnvFile(t, `SERIAL_PORT="/dev/ttyUSB0"\n`)
	_, err := UpdateEnvFile(p, map[string]string{
		"SERIAL_PORT": "/dev/ttyUSB1",
	})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	got := readEnvFile(t, p)
	if !strings.Contains(got, "SERIAL_PORT=/dev/ttyUSB1") {
		t.Errorf("кавычки не снялись:\n%s", got)
	}
	if strings.Contains(got, `"/dev/ttyUSB1"`) {
		t.Errorf("кавычки остались:\n%s", got)
	}
}

func TestUpdateEnvFileAddMissing(t *testing.T) {
	p := writeEnvFile(t, "DB_PATH=./data/sms.db\n")
	applied, err := UpdateEnvFile(p, map[string]string{
		"GAMMU_ROOT":  "/var/spool/sms",
		"SERIAL_PORT": "/dev/ttyUSB0",
	})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	if len(applied) != 2 {
		t.Errorf("applied=%v, ожидалось 2", applied)
	}

	got := readEnvFile(t, p)
	for _, want := range []string{
		"DB_PATH=./data/sms.db",
		"# Добавлено smsgateway detect-modem",
		"GAMMU_ROOT=/var/spool/sms",
		"SERIAL_PORT=/dev/ttyUSB0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет строки %q:\n%s", want, got)
		}
	}
	// Сортировка добавленных ключей детерминирована.
	gIdx := strings.Index(got, "GAMMU_ROOT=")
	sIdx := strings.Index(got, "SERIAL_PORT=")
	if gIdx < 0 || sIdx < 0 || gIdx > sIdx {
		t.Errorf("порядок добавленных ключей неверный:\n%s", got)
	}
}

func TestUpdateEnvFileUncomment(t *testing.T) {
	p := writeEnvFile(t, `# SERIAL_PORT=/dev/ttyUSB0
DB_PATH=./data/sms.db
`)
	applied, err := UpdateEnvFile(p, map[string]string{
		"SERIAL_PORT": "/dev/ttyUSB1",
	})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied=%v", applied)
	}
	got := readEnvFile(t, p)
	if !strings.Contains(got, "SERIAL_PORT=/dev/ttyUSB1") {
		t.Errorf("не раскомментировано:\n%s", got)
	}
	if strings.Contains(got, "# SERIAL_PORT=") {
		t.Errorf("комментарий остался:\n%s", got)
	}
	// DB_PATH не тронут.
	if !strings.Contains(got, "DB_PATH=./data/sms.db") {
		t.Errorf("DB_PATH потерян:\n%s", got)
	}
}

func TestUpdateEnvFileUncommentNoSpace(t *testing.T) {
	// Форма "#SERIAL_PORT=..." тоже раскомментируется.
	p := writeEnvFile(t, "#SERIAL_PORT=/dev/ttyUSB0\n")
	_, err := UpdateEnvFile(p, map[string]string{
		"SERIAL_PORT": "/dev/ttyUSB1",
	})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	got := readEnvFile(t, p)
	if !strings.Contains(got, "SERIAL_PORT=/dev/ttyUSB1") {
		t.Errorf("не раскомментировано:\n%s", got)
	}
}

func TestUpdateEnvFileCreateNew(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", ".env.new")

	applied, err := UpdateEnvFile(p, map[string]string{
		"SERIAL_PORT": "/dev/ttyUSB0",
	})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied=%v", applied)
	}
	got := readEnvFile(t, p)
	if !strings.Contains(got, "SERIAL_PORT=/dev/ttyUSB0") {
		t.Errorf("файл не создан корректно:\n%s", got)
	}
}

func TestUpdateEnvFileEmptyUpdates(t *testing.T) {
	p := writeEnvFile(t, "SERIAL_PORT=/dev/ttyUSB0\n")
	before := readEnvFile(t, p)

	applied, err := UpdateEnvFile(p, nil)
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	if applied != nil {
		t.Errorf("applied=%v, ожидалось nil", applied)
	}
	if readEnvFile(t, p) != before {
		t.Error("файл изменён при пустом updates")
	}
}

func TestUpdateEnvFileEmptyPath(t *testing.T) {
	if _, err := UpdateEnvFile("", map[string]string{"X": "1"}); err == nil {
		t.Fatal("ожидалась ошибка для пустого пути")
	}
}

func TestUpdateEnvFilePreservesBlankLines(t *testing.T) {
	p := writeEnvFile(t, "A=1\n\nB=2\n")
	_, err := UpdateEnvFile(p, map[string]string{"A": "9"})
	if err != nil {
		t.Fatalf("UpdateEnvFile: %v", err)
	}
	got := readEnvFile(t, p)
	// Проверим, что пустая строка между A и B сохранилась.
	if !strings.Contains(got, "A=9\n\nB=2") {
		t.Errorf("структура не сохранена:\n%q", got)
	}
}

func TestSplitEnvLine(t *testing.T) {
	cases := []struct {
		in        string
		key       string
		value     string
		commented bool
	}{
		{"KEY=value", "KEY", "value", false},
		{`KEY="value"`, "KEY", "value", false},
		{"  KEY = value  ", "KEY", "value", false},
		{"# KEY=value", "KEY", "value", true},
		{"#KEY=value", "KEY", "value", true},
		{"# comment only", "", "", true},
		{"", "", "", false},
		{"   ", "", "", false},
		{"no equals", "", "", false},
	}
	for _, c := range cases {
		k, v, cm := splitEnvLine(c.in)
		if k != c.key || v != c.value || cm != c.commented {
			t.Errorf("splitEnvLine(%q)=(%q,%q,%v), ожидалось (%q,%q,%v)",
				c.in, k, v, cm, c.key, c.value, c.commented)
		}
	}
}

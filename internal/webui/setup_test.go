package webui

import (
	"os"
	"testing"
)

// TestValidSetupPort — проверяет валидацию порта для /setup.
//
// Используем ТОЛЬКО заведомо несуществующие пути под
// whitelisted префиксами (номера 999 — ttyUSB/ttyACM бывают
// 0-255, 999 быть не может), чтобы тест не зависел от того,
// подключён ли модем на машине, где он запущен. Раньше тест
// проверял "/dev/ttyUSB0" → false, и падал на любой машине
// с реальным модемом.
func TestValidSetupPort(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Не whitelisted — всегда false.
		{"/dev/ttyS0", false},
		{"/dev/mem", false},
		{"", false},
		{"relative/path", false},
		{"ttyUSB0", false}, // без /dev — не whitelisted

		// Whitelisted, но заведомо несуществующие файлы — false
		// (os.Stat вернёт ENOENT).
		{"/dev/ttyUSB999", false},
		{"/dev/ttyACM999", false},
		{"/dev/serial/by-id/usb-does-not-exist-test-xyz", false},
		{"/dev/serial/by-path/does-not-exist-test-xyz", false},
	}
	for _, c := range cases {
		if got := validSetupPort(c.in); got != c.want {
			t.Errorf("validSetupPort(%q)=%v, ожидалось %v",
				c.in, got, c.want)
		}
	}
}

// TestValidSetupPortExisting — существующий файл под
// whitelisted префиксом даёт true.
//
// Создаём временный файл /dev/ttyUSB<suffix> (префикс
// подходит под whitelist). Если прав на запись в /dev нет
// (CI, обычный пользователь) — t.Skip.
func TestValidSetupPortExisting(t *testing.T) {
	f, err := os.CreateTemp("/dev", "ttyUSB-smsgateway-test-")
	if err != nil {
		t.Skipf("нет прав на запись в /dev: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(path) })

	if !validSetupPort(path) {
		t.Errorf("validSetupPort(%q)=false, ожидалось true "+
			"(файл существует)", path)
	}
}

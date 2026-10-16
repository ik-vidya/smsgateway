package modem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSys создаёт минимальное дерево /sys и /dev/serial/by-id
// внутри tmpdir, чтобы DiscoverPorts читал его через fs.FS.
type fakeSys struct {
	root string
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	return &fakeSys{root: t.TempDir()}
}

// addPort создаёт /sys/class/tty/<name>/device/* с указанными
// метаданными. Пустые значения пропускаются.
func (f *fakeSys) addPort(t *testing.T, name string,
	vid, pid, serial, manufacturer, product string) {

	t.Helper()
	dir := filepath.Join(f.root, "sys/class/tty", name, "device")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %q: %v", dir, err)
	}
	write := func(file, val string) {
		if val == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, file),
			[]byte(val+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", file, err)
		}
	}
	write("idVendor", vid)
	write("idProduct", pid)
	write("serial", serial)
	write("manufacturer", manufacturer)
	write("product", product)
}

// addByID создаёт файл /dev/serial/by-id/<name> с содержимым —
// целевым путём. Имитация симлинка, но в fs.FS достаточно файла
// с текстом.
func (f *fakeSys) addByID(t *testing.T, name, target string) {
	t.Helper()
	dir := filepath.Join(f.root, "dev/serial/by-id")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %q: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name),
		[]byte(target+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func (f *fakeSys) addByPath(t *testing.T, name, target string) {
	t.Helper()
	dir := filepath.Join(f.root, "dev/serial/by-path")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %q: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name),
		[]byte(target+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func (f *fakeSys) fsys() *os.Root {
	return nil // не используется, оставлено для читателя
}

// ==================== tests ====================

func TestDiscoverSkipAT(t *testing.T) {
	f := newFakeSys(t)
	f.addPort(t, "ttyUSB0", "10c4", "ea60", "0001",
		"Silicon Labs", "CP2102 USB to UART Bridge")

	infos, err := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:   os.DirFS(f.root),
		Paths:  []string{"/dev/ttyUSB0"},
		SkipAT: true,
	})
	if err != nil {
		t.Fatalf("DiscoverPorts: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len=%d", len(infos))
	}
	got := infos[0]
	if got.Port != "/dev/ttyUSB0" {
		t.Errorf("Port=%q", got.Port)
	}
	if got.VID != "10c4" || got.PID != "ea60" {
		t.Errorf("VID/PID=%q/%q", got.VID, got.PID)
	}
	if got.SerialNumber != "0001" {
		t.Errorf("Serial=%q", got.SerialNumber)
	}
	if got.Manufacturer != "Silicon Labs" {
		t.Errorf("Manufacturer=%q", got.Manufacturer)
	}
	if got.Product != "CP2102 USB to UART Bridge" {
		t.Errorf("Product=%q", got.Product)
	}
	if !got.Skipped {
		t.Error("Skipped=false, ожидалось true")
	}
}

func TestDiscoverMultiplePorts(t *testing.T) {
	f := newFakeSys(t)
	f.addPort(t, "ttyUSB0", "10c4", "ea60", "AAA", "", "")
	f.addPort(t, "ttyUSB1", "1a86", "7523", "BBB", "", "")

	infos, err := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:   os.DirFS(f.root),
		Paths:  []string{"/dev/ttyUSB1", "/dev/ttyUSB0"}, // порядок на входе
		SkipAT: true,
	})
	if err != nil {
		t.Fatalf("DiscoverPorts: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len=%d", len(infos))
	}
	// Порядок должен быть отсортирован.
	if infos[0].Port != "/dev/ttyUSB0" || infos[1].Port != "/dev/ttyUSB1" {
		t.Errorf("порядок: %q, %q", infos[0].Port, infos[1].Port)
	}
}

func TestDiscoverStablePathByID(t *testing.T) {
	f := newFakeSys(t)
	f.addPort(t, "ttyUSB0", "10c4", "ea60", "0001", "", "")
	f.addByID(t, "usb-Silicon_Labs_CP2102_USB_to_UART_Bridge_Controller_0001-if00-port0",
		"/dev/ttyUSB0")

	infos, _ := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:   os.DirFS(f.root),
		Paths:  []string{"/dev/ttyUSB0"},
		SkipAT: true,
	})
	if infos[0].StablePath !=
		"/dev/serial/by-id/usb-Silicon_Labs_CP2102_USB_to_UART_Bridge_Controller_0001-if00-port0" {
		t.Errorf("StablePath=%q", infos[0].StablePath)
	}
}

func TestDiscoverStablePathByPath(t *testing.T) {
	f := newFakeSys(t)
	f.addPort(t, "ttyUSB0", "10c4", "ea60", "0001", "", "")
	f.addByPath(t, "pci-0000:00:14.0-usb-0:1.2:1.0-port0", "/dev/ttyUSB0")

	infos, _ := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:   os.DirFS(f.root),
		Paths:  []string{"/dev/ttyUSB0"},
		SkipAT: true,
	})
	if infos[0].ByPathPath !=
		"/dev/serial/by-path/pci-0000:00:14.0-usb-0:1.2:1.0-port0" {
		t.Errorf("ByPathPath=%q", infos[0].ByPathPath)
	}
}

func TestDiscoverStablePathRelativeTarget(t *testing.T) {
	// Симлинк может указывать ../ttyUSB0 — нормализуем.
	f := newFakeSys(t)
	f.addPort(t, "ttyUSB0", "", "", "", "", "")
	f.addByID(t, "usb-test", "../../ttyUSB0")

	infos, _ := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:   os.DirFS(f.root),
		Paths:  []string{"/dev/ttyUSB0"},
		SkipAT: true,
	})
	if infos[0].StablePath != "/dev/serial/by-id/usb-test" {
		t.Errorf("StablePath=%q", infos[0].StablePath)
	}
}

func TestDiscoverNoSysMetadata(t *testing.T) {
	// Порт есть, но /sys пуст — метаданные пустые, не падаем.
	f := newFakeSys(t)

	infos, err := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:   os.DirFS(f.root),
		Paths:  []string{"/dev/ttyUSB0"},
		SkipAT: true,
	})
	if err != nil {
		t.Fatalf("DiscoverPorts: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len=%d", len(infos))
	}
	if infos[0].VID != "" || infos[0].SerialNumber != "" {
		t.Errorf("метаданные не пусты: %+v", infos[0])
	}
}

func TestDiscoverEmpty(t *testing.T) {
	f := newFakeSys(t)
	infos, err := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:   os.DirFS(f.root),
		Paths:  []string{},
		SkipAT: true,
	})
	if err != nil {
		t.Fatalf("DiscoverPorts: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("len=%d, ожидалось 0", len(infos))
	}
}

func TestDiscoverATErrorWhenPortMissing(t *testing.T) {
	// SkipAT=false, но порт на реальной ФС не существует —
	// должна быть запись в ATError, не паника и не ошибка наружу.
	infos, err := DiscoverPorts(context.Background(), DiscoverConfig{
		Root:      os.DirFS(t.TempDir()),
		Paths:     []string{"/dev/does-not-exist-xyz"},
		ATTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DiscoverPorts: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len=%d", len(infos))
	}
	if infos[0].ATError == "" {
		t.Error("ATError пуст, ожидалась запись об ошибке открытия")
	}
	if infos[0].Skipped {
		t.Error("Skipped=true, но SkipAT=false")
	}
}

func TestResolveSymlinkTarget(t *testing.T) {
	const dir = "dev/serial/by-id"

	cases := []struct {
		target string
		want   string
	}{
		{"/dev/ttyUSB0", "/dev/ttyUSB0"},
		{"../../ttyUSB0", "/dev/ttyUSB0"},
		{"../ttyUSB1", "/dev/serial/ttyUSB1"},
		{"../../../dev/ttyUSB0", "/dev/ttyUSB0"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := resolveSymlinkTarget(dir, c.target); got != c.want {
			t.Errorf("resolveSymlinkTarget(%q, %q)=%q, ожидалось %q",
				dir, c.target, got, c.want)
		}
	}
}

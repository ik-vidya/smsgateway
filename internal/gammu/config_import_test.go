package gammu

import (
	"errors"
	"strings"
	"testing"
)

const sampleSMSDrc = `# Sample gammu-smsdrc
[gammu]
Connection = serial
Device = /dev/ttyUSB0
Pin = 1234
LogFile = /var/log/gammu.log
DebugLevel = 1

[smsd]
Service = FILES
InboxPath = /var/spool/sms/inbox/
OutboxPath = /var/spool/sms/outbox/
SentSMSPath = /var/spool/sms/sent/
ErrorSMSPath = /var/spool/sms/error/
InboxFormat = unicode
MaxRetries = 3
DeliveryReport = yes
LoopSleep = 5
`

func mustParse(t *testing.T, s string) map[string]map[string]string {
	t.Helper()
	cfg, err := ParseSMSDConfig(strings.NewReader(s))
	if err != nil {
		t.Fatalf("ParseSMSDConfig: %v", err)
	}
	return cfg
}

func TestParseSMSDConfig(t *testing.T) {
	cfg := mustParse(t, sampleSMSDrc)

	if cfg["gammu"]["Device"] != "/dev/ttyUSB0" {
		t.Errorf("Device=%q", cfg["gammu"]["Device"])
	}
	if cfg["gammu"]["Connection"] != "serial" {
		t.Errorf("Connection=%q", cfg["gammu"]["Connection"])
	}
	if cfg["smsd"]["Service"] != "FILES" {
		t.Errorf("Service=%q", cfg["smsd"]["Service"])
	}
	if cfg["smsd"]["MaxRetries"] != "3" {
		t.Errorf("MaxRetries=%q", cfg["smsd"]["MaxRetries"])
	}
}

func TestParseSMSDConfigComments(t *testing.T) {
	src := `# comment
; another comment
[smsd]
Service = FILES
# inline comment above
InboxPath = /var/spool/sms/inbox
`
	cfg := mustParse(t, src)
	if cfg["smsd"]["Service"] != "FILES" {
		t.Errorf("Service=%q", cfg["smsd"]["Service"])
	}
	if cfg["smsd"]["InboxPath"] != "/var/spool/sms/inbox" {
		t.Errorf("InboxPath=%q", cfg["smsd"]["InboxPath"])
	}
}

func TestParseSMSDConfigQuoted(t *testing.T) {
	src := `[gammu]
Device = "/dev/ttyUSB0"
Pin = "1234"
`
	cfg := mustParse(t, src)
	if cfg["gammu"]["Device"] != "/dev/ttyUSB0" {
		t.Errorf("Device=%q", cfg["gammu"]["Device"])
	}
	if cfg["gammu"]["Pin"] != "1234" {
		t.Errorf("Pin=%q", cfg["gammu"]["Pin"])
	}
}

// ==================== ConvertSMSDConfig ====================

func TestConvertBasic(t *testing.T) {
	cfg := mustParse(t, sampleSMSDrc)
	res, err := ConvertSMSDConfig(cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	want := map[string]string{
		"SERIAL_PORT":               "/dev/ttyUSB0",
		"MODEM_PIN":                 "1234",
		"GAMMU_ROOT":                "/var/spool/sms",
		"GAMMU_INBOX":               "inbox",
		"GAMMU_OUTBOX":              "outbox",
		"GAMMU_SENT":                "sent",
		"GAMMU_ERROR":               "error",
		"GAMMU_INBOX_FORMAT":        "unicode",
		"GAMMU_OUTBOX_MAX_ATTEMPTS": "4",
	}
	for k, v := range want {
		if res.Env[k] != v {
			t.Errorf("%s=%q, ожидалось %q", k, res.Env[k], v)
		}
	}

	ignored := make(map[string]bool)
	for _, o := range res.Ignored {
		ignored[o.Section+"/"+o.Key] = true
	}
	for _, want := range []string{
		"gammu/LogFile", "gammu/DebugLevel",
		"smsd/DeliveryReport", "smsd/LoopSleep",
	} {
		if !ignored[want] {
			t.Errorf("не в Ignored: %s", want)
		}
	}
}

func TestConvertConnectionIrdaRejected(t *testing.T) {
	src := `[gammu]
Connection = irda
Device = IrDA:0
[smsd]
Service = FILES
`
	cfg := mustParse(t, src)
	_, err := ConvertSMSDConfig(cfg)
	if err == nil {
		t.Fatal("ожидалась ошибка для irda")
	}
	var ce *GammuConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("ожидался *GammuConfigError, получено %T", err)
	}
	if ce.Key != "Connection" {
		t.Errorf("Key=%q", ce.Key)
	}
}

func TestConvertConnectionBluetoothRejected(t *testing.T) {
	src := `[gammu]
Connection = bluetooth
Device = 00:11:22:33:44:55
[smsd]
Service = FILES
`
	cfg := mustParse(t, src)
	_, err := ConvertSMSDConfig(cfg)
	if err == nil {
		t.Fatal("ожидалась ошибка для bluetooth")
	}
}

func TestConvertConnectionAtAndSerialOK(t *testing.T) {
	for _, conn := range []string{"serial", "at", "SERIAL", "AT"} {
		src := "[gammu]\nConnection = " + conn +
			"\nDevice = /dev/ttyUSB0\n[smsd]\nService = FILES\n"
		cfg := mustParse(t, src)
		if _, err := ConvertSMSDConfig(cfg); err != nil {
			t.Errorf("Connection=%s: %v", conn, err)
		}
	}
}

// TestConvertNoCommonRoot: у абсолютных путей нет общего родителя.
// Ожидаем: GAMMU_ROOT пуст, пути записаны абсолютными, есть
// предупреждение.
func TestConvertNoCommonRoot(t *testing.T) {
	src := `[gammu]
Device = /dev/ttyUSB0
[smsd]
Service = FILES
InboxPath = /var/spool/sms/inbox
OutboxPath = /opt/smsgw/outbox
SentSMSPath = /srv/sent
ErrorSMSPath = /tmp/err
`
	cfg := mustParse(t, src)
	res, err := ConvertSMSDConfig(cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	if res.Env["GAMMU_ROOT"] != "" {
		t.Errorf("GAMMU_ROOT=%q, ожидалось пусто", res.Env["GAMMU_ROOT"])
	}
	wantAbs := map[string]string{
		"GAMMU_INBOX":  "/var/spool/sms/inbox",
		"GAMMU_OUTBOX": "/opt/smsgw/outbox",
		"GAMMU_SENT":   "/srv/sent",
		"GAMMU_ERROR":  "/tmp/err",
	}
	for k, v := range wantAbs {
		if res.Env[k] != v {
			t.Errorf("%s=%q, ожидалось %q", k, res.Env[k], v)
		}
	}

	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "общего родителя") {
			found = true
		}
	}
	if !found {
		t.Errorf("ожидалось предупреждение об отсутствии общего родителя: %v",
			res.Warnings)
	}
}

// TestConvertCommonRootWithSubdirs: /var/spool/sms/{inbox,outbox,sent,error}.
// Ожидаем GAMMU_ROOT=/var/spool/sms (со слешем!) и подкаталоги
// inbox/outbox/sent/error.
func TestConvertCommonRootWithSubdirs(t *testing.T) {
	src := `[gammu]
Device = /dev/ttyUSB0
[smsd]
Service = FILES
InboxPath = /var/spool/sms/inbox/
OutboxPath = /var/spool/sms/outbox/
SentSMSPath = /var/spool/sms/sent/
ErrorSMSPath = /var/spool/sms/error/
`
	cfg := mustParse(t, src)
	res, _ := ConvertSMSDConfig(cfg)
	if res.Env["GAMMU_ROOT"] != "/var/spool/sms" {
		t.Errorf("GAMMU_ROOT=%q, ожидалось /var/spool/sms",
			res.Env["GAMMU_ROOT"])
	}
	want := map[string]string{
		"GAMMU_INBOX":  "inbox",
		"GAMMU_OUTBOX": "outbox",
		"GAMMU_SENT":   "sent",
		"GAMMU_ERROR":  "error",
	}
	for k, v := range want {
		if res.Env[k] != v {
			t.Errorf("%s=%q, ожидалось %q", k, res.Env[k], v)
		}
	}
}

// TestConvertPartialCommonRoot: все четыре пути под /var/spool,
// но sent и error — на уровень выше.
func TestConvertPartialCommonRoot(t *testing.T) {
	src := `[gammu]
Device = /dev/ttyUSB0
[smsd]
Service = FILES
InboxPath = /var/spool/sms/inbox
OutboxPath = /var/spool/sms/outbox
SentSMSPath = /var/spool/sent
ErrorSMSPath = /var/spool/error
`
	cfg := mustParse(t, src)
	res, _ := ConvertSMSDConfig(cfg)
	if res.Env["GAMMU_ROOT"] != "/var/spool" {
		t.Errorf("GAMMU_ROOT=%q, ожидалось /var/spool",
			res.Env["GAMMU_ROOT"])
	}
	want := map[string]string{
		"GAMMU_INBOX":  "sms/inbox",
		"GAMMU_OUTBOX": "sms/outbox",
		"GAMMU_SENT":   "sent",
		"GAMMU_ERROR":  "error",
	}
	for k, v := range want {
		if res.Env[k] != v {
			t.Errorf("%s=%q, ожидалось %q", k, res.Env[k], v)
		}
	}
}

// TestConvertMixedPaths: смешение абсолютных и относительных.
// Абсолютные имеют общего родителя /var/spool/sms — он становится
// GAMMU_ROOT. Относительные (sent, error) остаются как есть и в
// рантайме склеятся с root.
func TestConvertMixedPaths(t *testing.T) {
	src := `[gammu]
Device = /dev/ttyUSB0
[smsd]
Service = FILES
InboxPath = /var/spool/sms/inbox
OutboxPath = /var/spool/sms/outbox
SentSMSPath = sent
ErrorSMSPath = error
`
	cfg := mustParse(t, src)
	res, _ := ConvertSMSDConfig(cfg)
	if res.Env["GAMMU_ROOT"] != "/var/spool/sms" {
		t.Errorf("GAMMU_ROOT=%q", res.Env["GAMMU_ROOT"])
	}
	want := map[string]string{
		"GAMMU_INBOX":  "inbox",
		"GAMMU_OUTBOX": "outbox",
		"GAMMU_SENT":   "sent",
		"GAMMU_ERROR":  "error",
	}
	for k, v := range want {
		if res.Env[k] != v {
			t.Errorf("%s=%q, ожидалось %q", k, res.Env[k], v)
		}
	}
}

// TestConvertAllRelative: все пути относительные — GAMMU_ROOT
// определить нельзя, всё пишется как есть, есть предупреждение.
func TestConvertAllRelative(t *testing.T) {
	src := `[gammu]
Device = /dev/ttyUSB0
[smsd]
Service = FILES
InboxPath = inbox
OutboxPath = outbox
SentSMSPath = sent
ErrorSMSPath = error
`
	cfg := mustParse(t, src)
	res, _ := ConvertSMSDConfig(cfg)
	if res.Env["GAMMU_ROOT"] != "" {
		t.Errorf("GAMMU_ROOT=%q, ожидалось пусто", res.Env["GAMMU_ROOT"])
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "относительные") {
			found = true
		}
	}
	if !found {
		t.Errorf("ожидалось предупреждение: %v", res.Warnings)
	}
}

func TestConvertServiceNotFilesWarns(t *testing.T) {
	src := `[gammu]
Device = /dev/ttyUSB0
[smsd]
Service = MYSQL
`
	cfg := mustParse(t, src)
	res, err := ConvertSMSDConfig(cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "FILES") {
			found = true
		}
	}
	if !found {
		t.Error("ожидалось предупреждение про Service != FILES")
	}
}

func TestConvertMaxRetriesIncrements(t *testing.T) {
	cases := map[string]string{
		"0": "1",
		"1": "2",
		"5": "6",
	}
	for in, want := range cases {
		src := "[gammu]\nDevice = /dev/tty\n[smsd]\nService = FILES\nMaxRetries = " + in + "\n"
		cfg := mustParse(t, src)
		res, _ := ConvertSMSDConfig(cfg)
		if res.Env["GAMMU_OUTBOX_MAX_ATTEMPTS"] != want {
			t.Errorf("MaxRetries=%s: attempts=%q, ожидалось %q",
				in, res.Env["GAMMU_OUTBOX_MAX_ATTEMPTS"], want)
		}
	}
}

func TestConvertInboxFormatMapping(t *testing.T) {
	cases := map[string]string{
		"standard": "utf8",
		"unicode":  "unicode",
		"utf8":     "utf8",
		"detail":   "detail",
	}
	for in, want := range cases {
		src := "[gammu]\nDevice = /dev/tty\n[smsd]\nService = FILES\nInboxFormat = " + in + "\n"
		cfg := mustParse(t, src)
		res, _ := ConvertSMSDConfig(cfg)
		if res.Env["GAMMU_INBOX_FORMAT"] != want {
			t.Errorf("InboxFormat=%s: %q, ожидалось %q",
				in, res.Env["GAMMU_INBOX_FORMAT"], want)
		}
	}
}

func TestConvertPINPriority(t *testing.T) {
	src := `[gammu]
Device = /dev/tty
Pin = 1111
[smsd]
Service = FILES
PIN = 2222
`
	cfg := mustParse(t, src)
	res, _ := ConvertSMSDConfig(cfg)
	if res.Env["MODEM_PIN"] != "2222" {
		t.Errorf("MODEM_PIN=%q, ожидалось 2222", res.Env["MODEM_PIN"])
	}
}

func TestConvertRenderEnv(t *testing.T) {
	cfg := mustParse(t, sampleSMSDrc)
	res, _ := ConvertSMSDConfig(cfg)
	out := res.RenderEnv("/etc/gammu-smsdrc")

	for _, want := range []string{
		"# Сгенерировано из /etc/gammu-smsdrc",
		"SERIAL_PORT=/dev/ttyUSB0",
		"MODEM_PIN=1234",
		"GAMMU_ROOT=/var/spool/sms",
		"GAMMU_INBOX=inbox",
		"GAMMU_INBOX_FORMAT=unicode",
		"GAMMU_OUTBOX_MAX_ATTEMPTS=4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет строки %q в выводе:\n%s", want, out)
		}
	}
}

func TestConvertEmptyConfig(t *testing.T) {
	cfg := mustParse(t, "")
	res, err := ConvertSMSDConfig(cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(res.Env) != 0 {
		t.Errorf("Env не пуст: %v", res.Env)
	}
}

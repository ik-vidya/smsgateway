package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.vidya.lv/vidya/smsgateway/internal/profiles"
)

// mockModem — карта command → response/error.
//
// Ответы — как у реального *modem.Modem.Exec: чистый payload
// без OK. StripOK в probe.exec дополнительно страхует от
// транспорта, который не стрипает (см. TestStripOK).
type mockModem struct {
	responses map[string]string
	errs      map[string]error
}

func (m *mockModem) Exec(_ context.Context, cmd string) (string, error) {
	if err, ok := m.errs[cmd]; ok {
		return "", err
	}
	if r, ok := m.responses[cmd]; ok {
		return r, nil
	}
	return "", errors.New("unknown command: " + cmd)
}

func fullMock() *mockModem {
	return &mockModem{
		responses: map[string]string{
			"ATI":       "SIM800 R14.18",
			"AT+CGMI":   "SIMCOM_Ltd",
			"AT+CGMM":   "SIMCOM_SIM800L",
			"AT+CGMR":   "Revision:1418B04SIM800L24",
			"AT+CGSN":   "865674030283119",
			"AT+CPIN?":  "+CPIN: READY",
			"AT+CCID":   "89371028006006960680",
			"AT+CIMI":   "247021003701891",
			"AT+SPIC":   "+SPIC: 3,3,10,10",
			"AT+CSQ":    "+CSQ: 17,0",
			"AT+CREG?":  `+CREG: 2,1,"00DF","156B"`,
			"AT+CGREG?": `+CGREG: 2,1,"00DF","156B"`,
			"AT+COPS?":  `+COPS: 0,0,"TELE2"`,
			"AT+CPMS?":  `+CPMS: "SM",0,50,"SM",0,50,"SM",0,25`,
			"AT+CMGF?":  "+CMGF: 0",
			"AT+CSMP?":  "+CSMP: 49,167,0,0",
			"AT+CNMI?":  "+CNMI: 2,1,0,1,0",
			"AT+CSCS?":  `+CSCS: "GSM"`,
			"AT+CBC":    "+CBC: 0,98,4191",
			"AT+CFUN?":  "+CFUN: 1",
			"AT+CBAND?": `+CBAND: "EGSM900"`,
		},
		errs: map[string]error{},
	}
}

func TestRunFull(t *testing.T) {
	r, err := Run(context.Background(), fullMock(), Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.ATI != "SIM800 R14.18" {
		t.Errorf("ATI=%q", r.ATI)
	}
	if r.Info.Manufacturer != "SIMCOM_Ltd" {
		t.Errorf("Manufacturer=%q", r.Info.Manufacturer)
	}
	if r.Info.Model != "SIMCOM_SIM800L" {
		t.Errorf("Model=%q", r.Info.Model)
	}
	if r.Profile.Name() != "sim800" {
		t.Errorf("Profile=%q, ожидалось sim800", r.Profile.Name())
	}
	if !r.HasSPIC {
		t.Error("HasSPIC=false при ответе +SPIC: 3,3,10,10")
	}
	if !r.HasCBC {
		t.Error("HasCBC=false при ответе +CBC: 0,98,4191")
	}
	if !r.HasCSMP || !r.HasCNMI || !r.HasCSCS || !r.HasCFUN || !r.HasCBAND {
		t.Errorf("не все Has* установлены: CSMP=%v CNMI=%v CSCS=%v "+
			"CFUN=%v CBAND=%v",
			r.HasCSMP, r.HasCNMI, r.HasCSCS, r.HasCFUN, r.HasCBAND)
	}
}

// TestRunFullWithOKLines — транспорт, который НЕ стрипает OK.
// probe.exec должен пострипать сам.
func TestRunFullWithOKLines(t *testing.T) {
	m := fullMock()
	// Перезаписываем: добавляем OK в конце.
	for k, v := range m.responses {
		m.responses[k] = v + "\nOK"
	}
	r, err := Run(context.Background(), m, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.ATI != "SIM800 R14.18" {
		t.Errorf("ATI=%q (OK не пострипан?)", r.ATI)
	}
	if strings.Contains(r.ATI, "OK") {
		t.Errorf("ATI содержит OK: %q", r.ATI)
	}
	if !r.HasCBC {
		t.Error("HasCBC=false при ответе с OK")
	}
}

func TestRunNoOptionalCommands(t *testing.T) {
	m := &mockModem{
		responses: map[string]string{
			"ATI":     "Generic Modem",
			"AT+CGMI": "Generic",
			"AT+CGMM": "Generic Modem",
			"AT+CGMR": "1.0",
			"AT+CGSN": "00000",
		},
		errs: map[string]error{
			"AT+SPIC":   errors.New("ERROR"),
			"AT+CBC":    errors.New("ERROR"),
			"AT+CSMP?":  errors.New("ERROR"),
			"AT+CNMI?":  errors.New("ERROR"),
			"AT+CSCS?":  errors.New("ERROR"),
			"AT+CFUN?":  errors.New("ERROR"),
			"AT+CBAND?": errors.New("ERROR"),
		},
	}
	r, err := Run(context.Background(), m, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, cmd := range []string{
		"AT+SPIC", "AT+CBC", "AT+CSMP?", "AT+CNMI?",
		"AT+CSCS?", "AT+CFUN?", "AT+CBAND?",
	} {
		if _, ok := r.Errors[cmd]; !ok {
			t.Errorf("%s не в Errors", cmd)
		}
	}
	if r.HasSPIC || r.HasCBC || r.HasCSMP || r.HasCNMI ||
		r.HasCSCS || r.HasCFUN || r.HasCBAND {
		t.Error("некоторые Has* установлены при ошибках")
	}
}

func TestRunScanNetworksNotCalled(t *testing.T) {
	m := fullMock()
	m.errs["AT+COPS=?"] = errors.New("should not be called")

	r, err := Run(context.Background(), m, Options{ScanNetworks: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.COPSRaw != "" {
		t.Errorf("COPSRaw=%q при ScanNetworks=false", r.COPSRaw)
	}
}

func TestRunScanNetworksCalled(t *testing.T) {
	m := fullMock()
	m.responses["AT+COPS=?"] =
		`+COPS: (1,"TELE2","TELE2","24702"),(3,"LMT","LMT","24701")`

	r, err := Run(context.Background(), m, Options{ScanNetworks: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(r.COPSRaw, "TELE2") {
		t.Errorf("COPSRaw=%q", r.COPSRaw)
	}
}

func TestRunNilModem(t *testing.T) {
	if _, err := Run(context.Background(), nil, Options{}); err == nil {
		t.Fatal("ожидалась ошибка для nil modem")
	}
}

func TestRunCmdTimeout(t *testing.T) {
	m := &slowModem{}
	opts := Options{CmdTimeout: 50 * time.Millisecond}

	r, err := Run(context.Background(), m, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := r.Errors["ATI"]; !ok {
		t.Errorf("ATI не в Errors, но должен был упасть по таймауту")
	}
}

// slowModem — блокируется до отмены контекста.
type slowModem struct{}

func (slowModem) Exec(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestStripOK(t *testing.T) {
	cases := map[string]string{
		"OK":                       "",
		"SIM800 R14.18\nOK":        "SIM800 R14.18",
		"+CSQ: 17,0\nOK":           "+CSQ: 17,0",
		"\n\n+CREG: 2,1\n\nOK\n\n": "+CREG: 2,1",
		"+CPMS: \"SM\",0,50\nOK":   `+CPMS: "SM",0,50`,
		"":                         "",
		"no ok here":               "no ok here",
	}
	for in, want := range cases {
		if got := stripOK(in); got != want {
			t.Errorf("stripOK(%q)=%q, ожидалось %q", in, got, want)
		}
	}
}

func TestBuildRecommendationsSIM800(t *testing.T) {
	p, _ := profiles.Get("sim800")
	r := &Result{
		Info:    profiles.Info{Manufacturer: "SIMCOM_Ltd", Model: "SIMCOM_SIM800L"},
		Profile: p,
	}
	r.Recommendations = buildRecommendations(r)
	joined := strings.Join(r.Recommendations, "\n")
	for _, want := range []string{
		"ForceUCS2ForAllSMS", "UseCSMPForSRR",
		"AlignReadStorage", "PinAttemptsAT",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("нет рекомендации про %s в:\n%s", want, joined)
		}
	}
}

func TestBuildRecommendationsGenericSIM800(t *testing.T) {
	p, _ := profiles.Get("generic")
	r := &Result{
		Info:    profiles.Info{Manufacturer: "SIMCOM_Ltd", Model: "SIM800L"},
		Profile: p,
	}
	r.Recommendations = buildRecommendations(r)
	joined := strings.Join(r.Recommendations, "\n")
	if !strings.Contains(joined, "MODEM_PROFILE=sim800") {
		t.Errorf("нет подсказки про sim800 в:\n%s", joined)
	}
}

// ==================== generate ====================

func TestGenerateProfile(t *testing.T) {
	r := &Result{
		Port: "/dev/ttyUSB0",
		Info: profiles.Info{Manufacturer: "Acme", Model: "ACME-1"},
	}
	code, err := r.GenerateProfile("acme")
	if err != nil {
		t.Fatalf("GenerateProfile: %v", err)
	}
	for _, want := range []string{
		"// generated by smsgateway probe",
		"package profiles",
		"type Acme struct{}",
		`func (Acme) Name() string { return "acme" }`,
		`strings.Contains(m, "ACME")`,
		`strings.Contains(mod, "ACME-1")`,
		"func (Acme) Workarounds() Workarounds",
		"func (Acme) PreInitHook",
		"func (Acme) PreSendHook",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("нет строки %q в:\n%s", want, code)
		}
	}
}

func TestGenerateProfileBadName(t *testing.T) {
	r := &Result{}
	for _, name := range []string{
		"", "Acme", "acme-test", "acme_1", "1acme", "ACME",
	} {
		if _, err := r.GenerateProfile(name); err == nil {
			t.Errorf("имя %q: ожидалась ошибка", name)
		}
	}
}

func TestWriteProfileNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acme.go")

	r := &Result{Port: "/dev/ttyUSB0",
		Info: profiles.Info{Manufacturer: "Acme", Model: "ACME-1"}}
	if err := r.WriteProfile(path, "acme", false); err != nil {
		t.Fatalf("WriteProfile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "type Acme struct") {
		t.Errorf("файл не создан: %s", raw)
	}
}

func TestWriteProfileRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acme.go")
	if err := os.WriteFile(path,
		[]byte("package profiles\n// manual file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &Result{}
	if err := r.WriteProfile(path, "acme", false); err == nil {
		t.Fatal("ожидалась ошибка: файл существует, force=false")
	}
}

func TestWriteProfileOverwritesGenerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acme.go")
	old := "// generated by smsgateway probe на основе данных...\n" +
		"package profiles\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &Result{Info: profiles.Info{Manufacturer: "Acme", Model: "ACME-1"}}
	if err := r.WriteProfile(path, "acme", false); err != nil {
		t.Fatalf("WriteProfile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "type Acme struct") {
		t.Errorf("файл не перезаписан")
	}
}

func TestWriteProfileForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acme.go")
	if err := os.WriteFile(path,
		[]byte("package profiles\n// manual\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &Result{Info: profiles.Info{Manufacturer: "Acme", Model: "ACME-1"}}
	if err := r.WriteProfile(path, "acme", true); err != nil {
		t.Fatalf("WriteProfile force: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "// generated by smsgateway probe") {
		t.Error("файл не перезаписан даже с force=true")
	}
}

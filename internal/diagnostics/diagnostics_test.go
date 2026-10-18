package diagnostics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type mockModem struct {
	responses map[string]string
	errs      map[string]error
}

func (m mockModem) Exec(_ context.Context, cmd string) (string, error) {
	if err, ok := m.errs[cmd]; ok {
		return "", err
	}
	if r, ok := m.responses[cmd]; ok {
		return r, nil
	}
	return "", errors.New("unknown command: " + cmd)
}

func fullMock() mockModem {
	return mockModem{
		responses: map[string]string{
			"AT+CGMI":   "SIMCOM_Ltd",
			"AT+CGMM":   "SIMCOM_SIM800L",
			"AT+CGMR":   "Revision:1418B04SIM800L24",
			"AT+CGSN":   "863584038586060",
			"AT+CIMI":   "248030023272107",
			"AT+CCID":   "89372038002032721077",
			"AT+CPIN?":  "+CPIN: READY",
			"AT+CSQ":    "+CSQ: 17,0",
			"AT+CREG?":  `+CREG: 2,5,"00DF","156B"`,
			"AT+CGREG?": `+CGREG: 2,5,"00DF","156B"`,
			"AT+COPS?":  `+COPS: 0,0,"TELE2"`,
			"AT+CSCA?":  `+CSCA: "2B33373235353039393131",145`,
			"AT+CPMS?":  `+CPMS: "SM",0,50,"SM",0,50,"SM",0,25`,
		},
		errs: map[string]error{},
	}
}

func TestCollectFull(t *testing.T) {
	r, err := Collect(context.Background(), fullMock())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if r.Manufacturer != "SIMCOM_Ltd" {
		t.Errorf("Manufacturer=%q", r.Manufacturer)
	}
	if r.Model != "SIMCOM_SIM800L" {
		t.Errorf("Model=%q", r.Model)
	}
	if r.Revision != "Revision:1418B04SIM800L24" {
		t.Errorf("Revision=%q", r.Revision)
	}
	if r.IMEI != "863584038586060" {
		t.Errorf("IMEI=%q", r.IMEI)
	}
	if r.IMSI != "248030023272107" {
		t.Errorf("IMSI=%q", r.IMSI)
	}
	if r.ICCID != "89372038002032721077" {
		t.Errorf("ICCID=%q", r.ICCID)
	}
	if r.SIMStatus != "READY" {
		t.Errorf("SIMStatus=%q", r.SIMStatus)
	}
	if r.Signal.RSSI != 17 || r.Signal.BER != 0 {
		t.Errorf("Signal=%+v", r.Signal)
	}
	if r.Signal.DBM != -113+2*17 {
		t.Errorf("DBM=%d, ожидалось %d", r.Signal.DBM, -113+2*17)
	}
	if r.Registration.Status != 5 {
		t.Errorf("Registration.Status=%d", r.Registration.Status)
	}
	if r.Registration.LAC != "00DF" {
		t.Errorf("Registration.LAC=%q", r.Registration.LAC)
	}
	if r.GPRSRegistration.Status != 5 {
		t.Errorf("GPRSRegistration.Status=%d", r.GPRSRegistration.Status)
	}
	if r.Operator.Name != "TELE2" {
		t.Errorf("Operator.Name=%q", r.Operator.Name)
	}
	if r.SMSC != "+3725509911" {
		t.Errorf("SMSC=%q", r.SMSC)
	}
	if r.Storage.Read != "SM" || r.Storage.Write != "SM" {
		t.Errorf("Storage=%+v", r.Storage)
	}
	if r.Storage.Total != 50 {
		t.Errorf("Storage.Total=%d", r.Storage.Total)
	}
}

func TestCollectPartialFailure(t *testing.T) {
	m := fullMock()
	m.errs["AT+CSQ"] = errors.New("timeout")
	m.errs["AT+CCID"] = errors.New("no sim")

	r, err := Collect(context.Background(), m)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if r.Manufacturer != "SIMCOM_Ltd" {
		t.Errorf("Manufacturer=%q", r.Manufacturer)
	}
	if r.Signal.RSSI != 0 {
		t.Errorf("Signal.RSSI=%d", r.Signal.RSSI)
	}
	if r.ICCID != "" {
		t.Errorf("ICCID=%q", r.ICCID)
	}
	if !strings.Contains(r.Raw["AT+CSQ"], "ERROR") {
		t.Errorf("Raw[AT+CSQ]=%q", r.Raw["AT+CSQ"])
	}
}

func TestParseSIMStatus(t *testing.T) {
	cases := map[string]string{
		"+CPIN: READY":   "READY",
		"+CPIN: SIM PIN": "SIM PIN",
		"READY":          "READY",
	}
	for in, want := range cases {
		if got := parseSIMStatus(in); got != want {
			t.Errorf("parseSIMStatus(%q)=%q, ожидалось %q", in, got, want)
		}
	}
}

func TestParseSignal(t *testing.T) {
	cases := []struct {
		in      string
		rssi    int
		quality string
	}{
		{"+CSQ: 25,0", 25, "отличный"},
		{"+CSQ: 17,0", 17, "хороший"},
		{"+CSQ: 12,0", 12, "удовлетворительный"},
		{"+CSQ: 7,0", 7, "слабый"},
		{"+CSQ: 2,0", 2, "очень слабый"},
		{"+CSQ: 99,0", 99, "нет данных"},
	}
	for _, c := range cases {
		got := parseSignal(c.in)
		if got.RSSI != c.rssi || got.Quality != c.quality {
			t.Errorf("parseSignal(%q)=%+v, ожидалось rssi=%d quality=%q",
				c.in, got, c.rssi, c.quality)
		}
	}
}

func TestParseCREG(t *testing.T) {
	cases := []struct {
		in   string
		stat int
		lac  string
		cid  string
	}{
		{`+CREG: 2,5,"00DF","156B"`, 5, "00DF", "156B"},
		{`+CREG: 2,0`, 0, "", ""},
		{`+CGREG: 2,5,"00DF","156B"`, 5, "00DF", "156B"},
	}
	for _, c := range cases {
		got := parseCREG(c.in)
		if got.Status != c.stat || got.LAC != c.lac || got.CID != c.cid {
			t.Errorf("parseCREG(%q)=%+v, ожидалось %+v", c.in, got, c)
		}
	}
}

func TestParseOperator(t *testing.T) {
	cases := []struct {
		in   string
		name string
		code string
	}{
		{`+COPS: 0,0,"TELE2"`, "TELE2", ""},
		{`+COPS: 0,2,"24702"`, "", "24702"},
	}
	for _, c := range cases {
		got := parseOperator(c.in)
		if got.Name != c.name || got.Code != c.code {
			t.Errorf("parseOperator(%q)=%+v", c.in, got)
		}
	}
}

func TestParseSMSC(t *testing.T) {
	got := parseSMSC(`+CSCA: "2B33373235353039393131",145`)
	if got != "+3725509911" {
		t.Errorf("parseSMSC=%q, ожидалось +3725509911", got)
	}
	got = parseSMSC(`+CSCA: "not-hex",145`)
	if got != "not-hex" {
		t.Errorf("parseSMSC=%q", got)
	}
}

func TestParseStorage(t *testing.T) {
	got := parseStorage(`+CPMS: "SM",0,50,"SM",0,50,"SM",0,25`)
	if got.Read != "SM" || got.Write != "SM" {
		t.Errorf("Storage=%+v", got)
	}
	if got.Used != 0 || got.Total != 50 {
		t.Errorf("Storage.Used=%d Total=%d", got.Used, got.Total)
	}
}

// TestParseSMSCUCS2 — SIM800L при CSCS=UCS2 отдаёт SMSC
// в виде hex UCS-2 (UTF-16BE): "002B0033003700320035003500300039..."
// = "+3725509911". До фикса parseSMSC возвращал исходный hex,
// потому что байты идут с NUL между символами, и isPrintable
// на ASCII-декодировании возвращал false.
func TestParseSMSCUCS2(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// +3725509911 в UCS-2 hex.
		{`+CSCA: "002B0033003700320035003500300039003900310031",145`,
			"+3725509911"},
		// +37121234567 в UCS-2 hex.
		// 12 символов × 4 hex = 48 hex-символов,
		// чётная длина. Раньше в этой строке был лишний
		// "00" в конце (50 hex, 25 байт) — decodeUCS2BE
		// возвращал false, parseSMSC отдавал hex, тест падал.
		{`+CSCA: "002B00330037003100320031003200330034003500360037",145`,
			"+37121234567"},
	}
	for _, c := range cases {
		got := parseSMSC(c.in)
		if got != c.want {
			t.Errorf("parseSMSC(%q)=%q, ожидалось %q", c.in, got, c.want)
		}
	}
}

// TestParseSMSCUCS2OddLength — регрессия: нечётная длина
// hex. decodeUCS2BE возвращает false, parseSMSC отдаёт
// исходный hex (не паникует).
func TestParseSMSCUCS2OddLength(t *testing.T) {
	// 25 байт (50 hex) — нечётная длина, невалидный UCS-2.
	in := `+CSCA: "002B0033003700310032003100320033003400350036003700",145`
	got := parseSMSC(in)
	want := "002B0033003700310032003100320033003400350036003700"
	if got != want {
		t.Errorf("parseSMSC(%q)=%q, ожидалось %q "+
			"(невалидный UCS-2 → исходный hex)", in, got, want)
	}
}

// TestParseSMSCAscii — ASCII-hex (когда CSCS не UCS2).
func TestParseSMSCAscii(t *testing.T) {
	// "2B33373235353039393131" = "+3725509911" в ASCII.
	in := `+CSCA: "2B33373235353039393131",145`
	if got := parseSMSC(in); got != "+3725509911" {
		t.Errorf("parseSMSC(%q)=%q", in, got)
	}
}

// TestParseSMSCPlain — CSCS=GSM, номер как есть.
func TestParseSMSCPlain(t *testing.T) {
	in := `+CSCA: "+3725509911",145`
	if got := parseSMSC(in); got != "+3725509911" {
		t.Errorf("parseSMSC(%q)=%q", in, got)
	}
}

// TestCollectTimeouts — общий бюджет diagTotalTimeout,
// на команду diagPerCmdTimeout. Мок блокируется до отмены
// контекста — проверяем, что Collect не висит дольше
// diagTotalTimeout.
func TestCollectTimeouts(t *testing.T) {
	start := time.Now()
	_, err := Collect(context.Background(), &slowDiagModem{})
	dur := time.Since(start)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	maxAllowed := diagTotalTimeout + 2*time.Second
	if dur > maxAllowed {
		t.Errorf("Collect занял %v, ожидалось не больше %v",
			dur, maxAllowed)
	}
}

// slowDiagModem — блокируется до отмены контекста.
type slowDiagModem struct{}

func (slowDiagModem) Exec(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestSanitizeValue(t *testing.T) {
	cases := map[string]string{
		"863584038586060":         "863584038586060",
		"863584038586060\nOK":     "863584038586060",
		"863584038586060\r\nOK":   "863584038586060",
		"  863584038586060  ":     "863584038586060",
		"0\r86060":                "086060",
		"\r\n\r\n863584038586060": "863584038586060",
		"":                        "",
		"\r\n":                    "",
	}
	for in, want := range cases {
		if got := sanitizeValue(in); got != want {
			t.Errorf("sanitizeValue(%q)=%q, ожидалось %q",
				in, got, want)
		}
	}
}

func TestIsValidIMEI(t *testing.T) {
	cases := map[string]bool{
		"863584038586060":  true,
		"086060":           false,
		"8635840385860600": false,
		"86358403858606A":  false,
		"":                 false,
	}
	for in, want := range cases {
		if got := isValidIMEI(in); got != want {
			t.Errorf("isValidIMEI(%q)=%v, ожидалось %v",
				in, got, want)
		}
	}
}

func TestCollectInvalidIMEIWarning(t *testing.T) {
	m := fullMock()
	m.responses["AT+CGSN"] = "0\r86060" // глюк UART
	r, err := Collect(context.Background(), m)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if r.IMEI != "086060" {
		t.Errorf("IMEI=%q, ожидалось 086060 (после sanitize)", r.IMEI)
	}
	if _, ok := r.Raw["AT+CGSN.warning"]; !ok {
		t.Error("нет warning в Raw для невалидного IMEI")
	}
}

// TestParseCREGEmpty — пустой ответ AT+CREG? (таймаут)
// даёт RegInfo с Error, а не «молчаливый» {status:0}.
func TestParseCREGEmpty(t *testing.T) {
	r := parseCREG("")
	if r.Status != 0 {
		t.Errorf("Status=%d, ожидалось 0", r.Status)
	}
	if r.Error != "no response (timeout or modem busy)" {
		t.Errorf("Error=%q", r.Error)
	}
}

// TestParseSignalEmpty — пустой ответ AT+CSQ.
func TestParseSignalEmpty(t *testing.T) {
	s := parseSignal("")
	if s.Error != "no response (timeout or modem busy)" {
		t.Errorf("Error=%q", s.Error)
	}
}

// TestParseOperatorEmpty — пустой ответ AT+COPS?.
func TestParseOperatorEmpty(t *testing.T) {
	o := parseOperator("")
	if o.Error != "no response (timeout or modem busy)" {
		t.Errorf("Error=%q", o.Error)
	}
}

// TestParseStorageEmpty — пустой ответ AT+CPMS?.
func TestParseStorageEmpty(t *testing.T) {
	s := parseStorage("")
	if s.Error != "no response (timeout or modem busy)" {
		t.Errorf("Error=%q", s.Error)
	}
}

// TestParseCREGUnparsable — ответ есть, но не парсится.
func TestParseCREGUnparsable(t *testing.T) {
	r := parseCREG("+CREG: 0") // без ,<stat>
	if r.Error == "" {
		t.Error("ожидался Error для непарсибельного ответа")
	}
	if r.Status != 0 {
		t.Errorf("Status=%d, ожидалось 0", r.Status)
	}
}

// TestParseCREGValidNoError — валидный ответ не выставляет
// Error.
func TestParseCREGValidNoError(t *testing.T) {
	r := parseCREG(`+CREG: 2,5,"00DC","04ED"`)
	if r.Error != "" {
		t.Errorf("Error=%q, ожидалось пусто", r.Error)
	}
	if r.Status != 5 {
		t.Errorf("Status=%d, ожидалось 5", r.Status)
	}
}

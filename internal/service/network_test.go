package service

import (
	"context"
	"testing"
)

func TestParseCREGStat(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{`+CREG: 2,1,"00DF","156B"`, 1, true},
		{`+CREG: 2,5,"00DF","156B"`, 5, true},
		{`+CREG: 0,2`, 2, true},
		{`+CREG: 0,0`, 0, true},
		{`+CREG: 2,3`, 3, true},
		{`+CGREG: 2,5,"00DF","156B"`, 5, true},
		{`garbage`, 0, false},
		{``, 0, false},
		{`+CREG: abc`, 0, false},
	}
	for _, c := range cases {
		got, ok := parseCREGStat(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseCREGStat(%q)=(%d,%v), ожидалось (%d,%v)",
				c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseCSQ(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{`+CSQ: 17,0`, 17, true},
		{`+CSQ: 0,0`, 0, true},
		{`+CSQ: 99,99`, 99, true},
		{`+CSQ: 31,0`, 31, true},
		{`garbage`, 0, false},
		{``, 0, false},
	}
	for _, c := range cases {
		got, ok := parseCSQ(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseCSQ(%q)=(%d,%v), ожидалось (%d,%v)",
				c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRegistered(t *testing.T) {
	svc := New(nil, nil, nil, Config{})

	// По умолчанию networkStat=0 → не зарегистрирован.
	if svc.Registered() {
		t.Error("Registered=true при networkStat=0")
	}

	cases := []struct {
		stat int
		want bool
	}{
		{0, false}, // не зарегистрирован
		{1, true},  // домашняя сеть
		{2, false}, // поиск
		{3, false}, // отказ
		{4, false}, // неизвестно
		{5, true},  // роуминг
	}
	for _, c := range cases {
		svc.mu.Lock()
		svc.networkStat = c.stat
		svc.mu.Unlock()
		if got := svc.Registered(); got != c.want {
			t.Errorf("Registered при stat=%d: %v, ожидалось %v",
				c.stat, got, c.want)
		}
	}
}

func TestRefreshNetwork(t *testing.T) {
	mock := newMockModem()
	mock.execResults = map[string]string{
		"AT+CREG?": `+CREG: 2,1,"00DF","156B"`,
		"AT+CSQ":   `+CSQ: 17,0`,
	}
	svc, _ := newTestService(t, mock)

	svc.refreshNetwork(context.Background())

	if !svc.Registered() {
		t.Error("после refreshNetwork (stat=1) Registered=false")
	}
	if got := svc.LastCSQ(); got != 17 {
		t.Errorf("LastCSQ=%d, ожидалось 17", got)
	}
}

func TestRefreshNetworkRoaming(t *testing.T) {
	mock := newMockModem()
	mock.execResults = map[string]string{
		"AT+CREG?": `+CREG: 2,5`,
		"AT+CSQ":   `+CSQ: 10,0`,
	}
	svc, _ := newTestService(t, mock)

	svc.refreshNetwork(context.Background())

	if !svc.Registered() {
		t.Error("роуминг (stat=5): Registered=false")
	}
	if got := svc.LastCSQ(); got != 10 {
		t.Errorf("LastCSQ=%d, ожидалось 10", got)
	}
}

func TestRefreshNetworkNotRegistered(t *testing.T) {
	mock := newMockModem()
	mock.execResults = map[string]string{
		"AT+CREG?": `+CREG: 0,2`, // поиск
		"AT+CSQ":   `+CSQ: 0,0`,  // нет сигнала
	}
	svc, _ := newTestService(t, mock)

	svc.refreshNetwork(context.Background())

	if svc.Registered() {
		t.Error("при stat=2 Registered=true")
	}
	if got := svc.LastCSQ(); got != 0 {
		t.Errorf("LastCSQ=%d, ожидалось 0", got)
	}
}

func TestRefreshNetworkMalformedResponse(t *testing.T) {
	mock := newMockModem()
	mock.execResults = map[string]string{
		"AT+CREG?": `garbage`,
		"AT+CSQ":   `garbage`,
	}
	svc, _ := newTestService(t, mock)

	// Начальные значения.
	svc.mu.Lock()
	svc.networkStat = 1
	svc.lastCSQ = 5
	svc.mu.Unlock()

	svc.refreshNetwork(context.Background())

	// Мусор в ответе — состояние не должно измениться.
	if !svc.Registered() {
		t.Error("состояние сброшено мусорным ответом")
	}
	if got := svc.LastCSQ(); got != 5 {
		t.Errorf("LastCSQ=%d, ожидалось 5 (не изменилось)", got)
	}
}

func TestRefreshNetworkNilModem(t *testing.T) {
	svc := New(nil, nil, nil, Config{})
	// Не должно паниковать.
	svc.refreshNetwork(context.Background())
}

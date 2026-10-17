package profiles

import (
	"context"
	"testing"
)

func TestGet(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"generic", "generic"},
		{"", "generic"},
		{"sim800", "sim800"},
		{"SIM800", "sim800"},
		{"Sim800", "sim800"},
		{"quectel", "quectel"},
		{"QUECTEL", "quectel"},
		{"telit", "telit"},
		{"Telit", "telit"},
		{"huawei", "huawei"},
		{"HUAWEI", "huawei"},
		{" huawei ", "huawei"},
	}
	for _, c := range cases {
		p, err := Get(c.name)
		if err != nil {
			t.Errorf("Get(%q): %v", c.name, err)
			continue
		}
		if p.Name() != c.want {
			t.Errorf("Get(%q).Name()=%q, ожидалось %q",
				c.name, p.Name(), c.want)
		}
	}
}

func TestGetUnknown(t *testing.T) {
	if _, err := Get("nokia"); err == nil {
		t.Fatal("ожидалась ошибка для неизвестного профиля")
	}
}

func TestAuto(t *testing.T) {
	cases := []struct {
		name string
		info Info
		want string
	}{
		{
			"sim800 by manufacturer",
			Info{Manufacturer: "SIMCOM_Ltd", Model: "SIMCOM_SIM800L"},
			"sim800",
		},
		{
			"sim800 by model",
			Info{Manufacturer: "", Model: "SIM800L"},
			"sim800",
		},
		{
			"sim900 by model",
			Info{Model: "SIM900"},
			"sim800",
		},
		{
			"quectel by manufacturer",
			Info{Manufacturer: "Quectel", Model: "EC25"},
			"quectel",
		},
		{
			"quectel by model prefix",
			Info{Model: "EC21"},
			"quectel",
		},
		{
			"telit by manufacturer",
			Info{Manufacturer: "Telit", Model: "LE910"},
			"telit",
		},
		{
			"telit by model prefix",
			Info{Model: "GL865"},
			"telit",
		},
		{
			"huawei by manufacturer",
			Info{Manufacturer: "Huawei", Model: "MS2131"},
			"huawei",
		},
		{
			"huawei by model prefix",
			Info{Model: "E3372"},
			"huawei",
		},
		{
			"unknown → generic",
			Info{Manufacturer: "Some", Model: "Thing"},
			"generic",
		},
		{
			"empty → generic",
			Info{},
			"generic",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Auto(c.info)
			if p.Name() != c.want {
				t.Errorf("Auto(%+v)=%q, ожидалось %q",
					c.info, p.Name(), c.want)
			}
		})
	}
}

// TestProfilesImplementInterface — компиляционная проверка, что
// все профили удовлетворяют Profile.
func TestProfilesImplementInterface(t *testing.T) {
	var _ Profile = Generic{}
	var _ Profile = SIM800{}
	var _ Profile = Quectel{}
	var _ Profile = Telit{}
	var _ Profile = Huawei{}
}

// stubHook — ModemHook-заглушка: собирает команды.
type stubHook struct {
	cmds []string
	resp string
}

func (s *stubHook) Exec(_ context.Context, cmd string) (string, error) {
	s.cmds = append(s.cmds, cmd)
	return s.resp, nil
}
func (s *stubHook) Log(string) {}

// TestQuectelPreHooksNoop — Quectel-профиль не шлёт лишних команд
// (наследует Generic).
func TestQuectelPreHooksNoop(t *testing.T) {
	for _, p := range []Profile{Quectel{}, Telit{}, Huawei{}} {
		h := &stubHook{}
		if err := p.PreInitHook(context.Background(), h); err != nil {
			t.Errorf("%s.PreInitHook: %v", p.Name(), err)
		}
		if err := p.PreSendHook(context.Background(), h); err != nil {
			t.Errorf("%s.PreSendHook: %v", p.Name(), err)
		}
		if len(h.cmds) != 0 {
			t.Errorf("%s: неожиданные команды: %v", p.Name(), h.cmds)
		}
	}
}

// TestWorkaroundsEmpty — у Quectel/Telit/Huawei нет обходов
// (наследуют пустой Workarounds от Generic).
func TestWorkaroundsEmpty(t *testing.T) {
	for _, p := range []Profile{Quectel{}, Telit{}, Huawei{}} {
		w := p.Workarounds()
		if w.ForceUCS2ForAllSMS || w.UseCSMPForSRR ||
			w.AlignReadStorage || w.PinAttemptsAT != "" {
			t.Errorf("%s.Workarounds не пуст: %+v", p.Name(), w)
		}
	}
}

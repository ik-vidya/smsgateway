package webui

import (
	"testing"
)

func TestValidSetupPort(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Whitelisted prefixes, но не существует — false.
		{"/dev/serial/by-id/usb-test", false},
		{"/dev/ttyUSB0", false},
		{"/dev/ttyACM0", false},
		// Не whitelisted — false независимо от существования.
		{"/dev/ttyS0", false},
		{"/dev/mem", false},
		{"", false},
		{"relative/path", false},
	}
	for _, c := range cases {
		if got := validSetupPort(c.in); got != c.want {
			t.Errorf("validSetupPort(%q)=%v, ожидалось %v",
				c.in, got, c.want)
		}
	}
}

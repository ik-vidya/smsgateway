package config

import (
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

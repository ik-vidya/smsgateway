package modem

import (
	"errors"
	"testing"
)

func TestIsTransientError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"temporary failure (text)", &ATError{Line: "+CMS ERROR: Temporary failure"}, true},
		{"no network service (text)", &ATError{Line: "+CMS ERROR: No network service"}, true},
		{"network timeout (text)", &ATError{Line: "+CMS ERROR: Network timeout"}, true},
		{"cms 38", &ATError{Line: "+CMS ERROR: 38"}, true},
		{"cms 41", &ATError{Line: "+CMS ERROR: 41"}, true},
		{"cms 42", &ATError{Line: "+CMS ERROR: 42"}, true},
		{"cms 47", &ATError{Line: "+CMS ERROR: 47"}, true},
		{"cms 331", &ATError{Line: "+CMS ERROR: 331"}, true},
		{"cms 332", &ATError{Line: "+CMS ERROR: 332"}, true},
		{"cms 500", &ATError{Line: "+CMS ERROR: 500"}, true},
		{"cms 305 (invalid dest)", &ATError{Line: "+CMS ERROR: 305"}, false},
		{"cms 21 (rejected)", &ATError{Line: "+CMS ERROR: 21"}, false},
		{"cme error 10", &ATError{Line: "+CME ERROR: 10"}, false},
		{"plain error", errors.New("что-то другое"), false},
		{"timeout error", errors.New("таймаут 5s ожидания ответа"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsTransientError(c.err)
			if got != c.want {
				t.Errorf("IsTransientError(%v)=%v, ожидалось %v",
					c.err, got, c.want)
			}
		})
	}
}

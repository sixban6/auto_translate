package test

import (
	"testing"

	"auto_translate/pkg/webtask"
)

func TestFormatHHMMSS(t *testing.T) {
	cases := []struct {
		sec      int
		expected string
	}{
		{sec: 0, expected: "00:00:00"},
		{sec: 59, expected: "00:00:59"},
		{sec: 3601, expected: "01:00:01"},
		{sec: 86399, expected: "23:59:59"},
		{sec: -5, expected: "00:00:00"},
	}

	for _, tc := range cases {
		got := webtask.FormatHHMMSS(tc.sec)
		if got != tc.expected {
			t.Fatalf("FormatHHMMSS(%d) = %s, expected %s", tc.sec, got, tc.expected)
		}
	}
}

package web

import (
	"strings"
	"testing"
)

func TestLoggedPathWithNoRuneStartKeepsNothing(t *testing.T) {
	if got := loggedPath(strings.Repeat("\x80", maxLoggedPath+1)); got != "" {
		t.Errorf("loggedPath = %q; want empty", got)
	}
}

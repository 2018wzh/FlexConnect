package auth

import (
	"strings"
	"testing"
)

func TestRedactAuthBodyRemovesCredentials(t *testing.T) {
	body := `<config-auth><session-token>token-value</session-token><auth><password>password-value</password></auth><message>ok</message></config-auth>`
	got := redactAuthBody(body)
	for _, secret := range []string{"token-value", "password-value", "<session-token>", "<password>"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted body still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "<message>ok</message>") {
		t.Fatalf("non-sensitive response content was removed: %s", got)
	}
}

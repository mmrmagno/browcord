package roomspec

import (
	"strings"
	"testing"
)

func TestValidIDRefusesAnythingThatCouldEscapeALabelOrPath(t *testing.T) {
	for _, bad := range []string{
		"", "../etc", "a/b", "a b", "a\nb", "a=b", "a,b", "a;b", "a\"b", "a%2fb", "é",
		strings.Repeat("a", 101),
	} {
		if ValidID(bad) == nil {
			t.Errorf("ValidID(%q) accepted", bad)
		}
	}
	for _, good := range []string{"main", "i-1234567890123456789-gc-1234-5678", "A_b-9"} {
		if err := ValidID(good); err != nil {
			t.Errorf("ValidID(%q) = %v", good, err)
		}
	}
}

func TestAgentTokenIsPerRoomAndPerSecret(t *testing.T) {
	a := AgentToken([]byte("secret-one"), "room-a")
	if ValidToken(a) != nil {
		t.Fatalf("derived token %q does not pass ValidToken", a)
	}
	if a == AgentToken([]byte("secret-one"), "room-b") {
		t.Fatal("two rooms share an agent token")
	}
	if a == AgentToken([]byte("secret-two"), "room-a") {
		t.Fatal("the token does not depend on the secret")
	}
	if a != AgentToken([]byte("secret-one"), "room-a") {
		t.Fatal("the token is not deterministic, a restarted gateway could not adopt its rooms")
	}
}

func TestValidTokenRefusesJunk(t *testing.T) {
	for _, bad := range []string{"", "abc", strings.Repeat("g", 64), strings.Repeat("A", 64), strings.Repeat("a", 65)} {
		if ValidToken(bad) == nil {
			t.Errorf("ValidToken(%q) accepted", bad)
		}
	}
}

func TestContainerNameIsFixedShape(t *testing.T) {
	n := ContainerName("i-123")
	if !strings.HasPrefix(n, NamePrefix) || len(n) != len(NamePrefix)+16 {
		t.Fatalf("ContainerName = %q", n)
	}
	if n == ContainerName("i-124") {
		t.Fatal("two rooms map to one container name")
	}
}

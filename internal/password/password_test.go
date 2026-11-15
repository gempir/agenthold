package password

import (
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	enc, err := New("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("encoded %q", enc)
	}
	h, err := Parse(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Verify("correct horse") {
		t.Fatal("right password rejected")
	}
	if h.Verify("correct horse ") || h.Verify("") {
		t.Fatal("wrong password accepted")
	}
	other, _ := New("correct horse")
	if other == enc {
		t.Fatal("salt not random")
	}
}

func TestParseRejects(t *testing.T) {
	for _, bad := range []string{
		"", "plaintext", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=18$m=65536,t=3,p=4$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=999999999,t=3,p=4$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=65536,t=3,p=4$!!$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

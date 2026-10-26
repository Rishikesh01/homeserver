//go:build integration

package secret_test

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/secret"
	"golang.org/x/crypto/argon2"
)

// TestPassword: the generated logins are the asked length, alphanumeric (so they survive .env
// files, URLs and shells unquoted) and not repeated.
func TestPassword(t *testing.T) {
	cases := []int{1, 16, 20, 40, 64}
	seen := map[string]bool{}
	for _, n := range cases {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			for i := 0; i < 50; i++ {
				p := secret.Password(n)
				if len(p) != n {
					t.Fatalf("len = %d, want %d", len(p), n)
				}
				if strings.Trim(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" {
					t.Fatalf("not alphanumeric: %q", p)
				}
				if n >= 16 && seen[p] {
					t.Fatalf("repeated password %q", p)
				}
				seen[p] = true
			}
		})
	}
}

// TestArgon2idPHC: the Vaultwarden admin token is stored as a PHC string a verifier can check
// (the parameters it embeds reproduce the hash), salted afresh each time, and escaped for compose.
func TestArgon2idPHC(t *testing.T) {
	cases := []struct{ token, wrong string }{
		{"correct horse battery staple", "correct horse battery stapl"},
		{secret.Password(40), "x"},
	}
	for _, tc := range cases {
		t.Run(tc.token[:4], func(t *testing.T) {
			phc := secret.Argon2idPHC(tc.token)
			if !verify(t, phc, tc.token) {
				t.Fatalf("%s doesn't verify its own token", phc)
			}
			if verify(t, phc, tc.wrong) {
				t.Error("a wrong token verified")
			}
			if again := secret.Argon2idPHC(tc.token); again == phc {
				t.Error("two hashes of one token are identical — the salt isn't random")
			}
			esc := secret.EscapeDollars(phc)
			if strings.Count(esc, "$$") != strings.Count(phc, "$") || strings.ReplaceAll(esc, "$$", "$") != phc {
				t.Errorf("EscapeDollars(%s) = %s", phc, esc)
			}
		})
	}
}

// verify checks token against a $argon2id$v=19$m=…,t=…,p=…$salt$hash string, the way Vaultwarden does.
func verify(t *testing.T, phc, token string) bool {
	t.Helper()
	f := strings.Split(phc, "$")
	if len(f) != 6 || f[1] != "argon2id" || f[2] != fmt.Sprintf("v=%d", argon2.Version) {
		t.Fatalf("not an argon2id PHC string: %s", phc)
	}
	var m, it uint32
	var p uint8
	if _, err := fmt.Sscanf(f[3], "m=%d,t=%d,p=%d", &m, &it, &p); err != nil {
		t.Fatalf("parameters %q: %v", f[3], err)
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(f[4])
	want, err2 := base64.RawStdEncoding.DecodeString(f[5])
	if err1 != nil || err2 != nil {
		t.Fatalf("salt/hash not base64: %v %v", err1, err2)
	}
	got := argon2.IDKey([]byte(token), salt, it, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

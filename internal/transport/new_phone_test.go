package transport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/vault"
)

func sessionFiles(t *testing.T, g *Google) (backups, leftovers []string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(g.path))
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(g.path)
	for _, e := range entries {
		switch name := e.Name(); {
		case name == base:
		case strings.HasPrefix(name, base+".before-new-phone-"):
			backups = append(backups, filepath.Join(filepath.Dir(g.path), name))
		default:
			leftovers = append(leftovers, name)
		}
	}
	return backups, leftovers
}

func TestPairNewPhoneSwapsInNewSessionAndKeepsOldCiphertext(t *testing.T) {
	g, original := cookieUpdateFixture(t, syntheticSession)
	newSession, err := g.vault.Seal("google-session", []byte(`{"mobile":{"user_id":"fixture-new-phone"}}`))
	if err != nil {
		t.Fatal(err)
	}
	err = g.replaceSession(func(path string) error {
		if path == g.path {
			t.Fatal("new phone paired over the current session")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("pairing target already exists")
		}
		return os.WriteFile(path, []byte(newSession), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(g.path)
	if string(current) != newSession {
		t.Fatal("new pairing not in place")
	}
	backups, leftovers := sessionFiles(t, g)
	if len(backups) != 1 || len(leftovers) != 0 {
		t.Fatal("expected exactly one backup and nothing else", backups, leftovers)
	}
	kept, _ := os.ReadFile(backups[0])
	if string(kept) != string(original) {
		t.Fatal("backup is not the exact old ciphertext")
	}
}

func TestPairNewPhoneFailureLeavesCurrentPairingUntouched(t *testing.T) {
	for name, pair := range map[string]func(string) error{
		"pairing fails":     func(string) error { return errors.New("pairing failed or approval timed out") },
		"nothing was saved": func(string) error { return nil },
		"save stopped early": func(path string) error {
			os.WriteFile(path+".tmp", []byte("partial"), 0600)
			return errors.New("cannot save session")
		},
		"partial file then failure": func(path string) error {
			os.WriteFile(path, []byte("partial"), 0600)
			return errors.New("pairing failed or approval timed out")
		},
	} {
		t.Run(name, func(t *testing.T) {
			g, original := cookieUpdateFixture(t, syntheticSession)
			if err := g.replaceSession(pair); err == nil {
				t.Fatal("failure reported as success")
			}
			current, _ := os.ReadFile(g.path)
			if string(current) != string(original) {
				t.Fatal("current pairing changed")
			}
			if backups, leftovers := sessionFiles(t, g); len(backups) != 0 || len(leftovers) != 0 {
				t.Fatal("failed attempt left files", backups, leftovers)
			}
		})
	}
}

func TestPairNewPhoneNeedsAnExistingPairingItCanRead(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	g := NewGoogle(v, filepath.Join(t.TempDir(), "google-session.enc"))
	called := false
	if err := g.replaceSession(func(string) error { called = true; return nil }); err == nil || !strings.Contains(err.Error(), "--pair") || called {
		t.Fatal("missing pairing not refused before pairing", err)
	}
	g, original := cookieUpdateFixture(t, syntheticSession)
	other, _ := vault.New(append(make([]byte, 31), 1))
	g.vault = other
	if err := g.replaceSession(func(string) error { called = true; return nil }); err == nil || called {
		t.Fatal("unreadable pairing not refused before pairing", err)
	}
	current, _ := os.ReadFile(g.path)
	if string(current) != string(original) {
		t.Fatal("refused attempt changed session")
	}
}

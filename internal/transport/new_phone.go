package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// PairNewPhone replaces the saved pairing with one for a different phone on the
// same Google account, such as after moving your number to a new phone.
//
// The new phone is paired into a separate file first, so a failed or timed-out
// pairing leaves the current one untouched. Only after it succeeds is the old
// encrypted session kept as a backup beside the new one, and the new one put in
// its place. Nothing is decrypted to make the backup.
func (g *Google) PairNewPhone(ctx context.Context, input string, showEmoji func(string)) error {
	return g.replaceSession(func(path string) error {
		return NewGoogle(g.vault, path).Pair(ctx, input, showEmoji)
	})
}

// replaceSession runs pair to write a new session at a fresh path and then
// swaps it in, keeping the old ciphertext as a backup.
func (g *Google) replaceSession(pair func(path string) error) error {
	if g.client != nil {
		return errors.New("pairing a new phone requires a stopped service")
	}
	original, err := os.ReadFile(g.path)
	if err != nil {
		return errors.New("no saved pairing to replace; use --pair for a first pairing")
	}
	if _, err = g.vault.Open("google-session", string(original)); err != nil {
		return errors.New("cannot decrypt saved pairing; check the master key")
	}
	dir, base := filepath.Dir(g.path), filepath.Base(g.path)
	// A unique name that does not exist yet: Pair refuses to overwrite.
	slot, err := os.CreateTemp(dir, base+".new-phone-*")
	if err != nil {
		return errors.New("cannot prepare new pairing; current pairing unchanged")
	}
	fresh := slot.Name()
	slot.Close()
	os.Remove(fresh)
	defer os.Remove(fresh)
	defer os.Remove(fresh + ".tmp") // Save's own temporary file, if it stopped early.
	if err = pair(fresh); err != nil {
		return err
	}
	if syncFile(fresh) != nil {
		return errors.New("new pairing was not saved; current pairing unchanged")
	}
	backup, err := os.CreateTemp(dir, base+".before-new-phone-*.enc")
	if err != nil {
		return errors.New("cannot create encrypted backup of the old pairing; current pairing unchanged")
	}
	_, err = backup.Write(original)
	if err == nil {
		err = backup.Sync()
	}
	closeErr := backup.Close()
	if err != nil || closeErr != nil {
		os.Remove(backup.Name())
		return errors.New("cannot save encrypted backup of the old pairing; current pairing unchanged")
	}
	if os.Rename(fresh, g.path) != nil {
		return errors.New("cannot replace the old pairing; it is unchanged and an encrypted backup was saved")
	}
	return nil
}

// syncFile flushes an existing file to disk, so the swap never puts a session
// in place that a crash could still lose.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

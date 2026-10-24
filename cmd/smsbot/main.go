package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/deusical/smsbot-server/internal/app"
	"github.com/deusical/smsbot-server/internal/archive"
	"github.com/deusical/smsbot-server/internal/httpapi"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"github.com/gofrs/flock"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// version is stamped at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "SmsBot: operation failed:", e)
		os.Exit(1)
	}
}
func run() error {
	return runWithInput(os.Args[1:], os.Stdin)
}

type options struct {
	showVersion         bool
	data, key           string
	listen, origin      string
	tlsCert, tlsKey     string
	backup              string
	archiveBackup       string
	archiveRestore      string
	archivePlain        bool
	archiveSince        string
	archiveConversation string
	restore             string
	ownerFile           string
	init                bool
	fixture             bool
	pair                bool
	updateCookies       bool
	cookiesStdin        bool
}

func parseOptions(args []string) (*options, error) {
	flags := flag.NewFlagSet("smsbot", flag.ContinueOnError)
	o := &options{}
	flags.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	flags.StringVar(&o.data, "data", "", "state directory outside source")
	flags.StringVar(&o.key, "key", "", "protected master key path outside state directory")
	flags.StringVar(&o.listen, "listen", "127.0.0.1:8787", "loopback listen address")
	flags.StringVar(&o.origin, "origin", "", "externally visible exact HTTPS origin for private access")
	flags.StringVar(&o.tlsCert, "tls-cert", "", "PEM TLS certificate path")
	flags.StringVar(&o.tlsKey, "tls-key", "", "PEM TLS private key path")
	flags.StringVar(&o.backup, "backup", "", "write encrypted offline backup to a new file")
	flags.StringVar(&o.archiveBackup, "archive-backup", "", "write streaming encrypted message archive to a new file")
	flags.StringVar(&o.archiveRestore, "archive-restore", "", "restore message archive offline into fresh initialized state")
	flags.BoolVar(&o.archivePlain, "archive-plaintext", false, "export readable unencrypted TAR with original files")
	flags.StringVar(&o.archiveSince, "archive-since", "", "archive export start date as RFC3339")
	flags.StringVar(&o.archiveConversation, "archive-conversation", "", "export one conversation (default all archived)")
	flags.StringVar(&o.restore, "restore", "", "restore encrypted backup into fresh initialized state")
	flags.StringVar(&o.ownerFile, "owner-file", "", "file for initial owner credential")
	flags.BoolVar(&o.init, "init", false, "initialize encrypted state and owner credential")
	flags.BoolVar(&o.fixture, "fixture", false, "explicit offline fixture mode")
	flags.BoolVar(&o.pair, "pair", false, "pair Google Messages locally with owner emoji approval")
	flags.BoolVar(&o.updateCookies, "update-cookies", false, "replace cookies in the existing encrypted pairing offline")
	flags.BoolVar(&o.cookiesStdin, "cookies-stdin", false, "read Google cookies from stdin, never command arguments")
	if e := flags.Parse(args); e != nil {
		return nil, e
	}
	return o, nil
}

func (o *options) validate() error {
	modes := 0
	for _, selected := range []bool{o.init, o.fixture, o.pair, o.updateCookies, o.backup != "", o.restore != "", o.archiveBackup != "", o.archiveRestore != ""} {
		if selected {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("initialization, fixture, pairing and cookie-update modes are mutually exclusive")
	}
	if (o.archiveSince != "" || o.archiveConversation != "" || o.archivePlain) && o.archiveBackup == "" {
		return errors.New("archive scope requires --archive-backup")
	}
	if o.cookiesStdin && !o.pair && !o.updateCookies {
		return errors.New("--cookies-stdin requires --pair or --update-cookies")
	}
	if (o.pair || o.updateCookies) && !o.cookiesStdin {
		return errors.New("pairing and cookie updates require --cookies-stdin")
	}
	if o.data == "" || o.key == "" {
		return errors.New("--data and --key are required")
	}
	return nil
}

// statePaths resolves the state directory and master key and rejects a key that
// lives inside the state directory.
func (o *options) statePaths() (string, string, error) {
	absData, e := filepath.Abs(o.data)
	if e != nil {
		return "", "", e
	}
	absKey, e := filepath.Abs(o.key)
	if e != nil {
		return "", "", e
	}
	rel, e := filepath.Rel(absData, absKey)
	if e != nil {
		return "", "", e
	}
	if rel == "." || (rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[:min(3, len(rel))] != ".."+string(os.PathSeparator)) {
		return "", "", errors.New("master key must be outside the state directory")
	}
	return absData, absKey, nil
}

func runWithInput(args []string, input io.Reader) error {
	o, e := parseOptions(args)
	if e != nil {
		return e
	}
	if o.showVersion {
		fmt.Println("smsbot", version)
		return nil
	}
	if e = o.validate(); e != nil {
		return e
	}
	absData, absKey, e := o.statePaths()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(absData, 0700); e != nil {
		return e
	}
	lock := flock.New(filepath.Join(absData, "service.lock"))
	ok, e := lock.TryLock()
	if e != nil {
		return errors.New("cannot lock state directory")
	}
	if !ok {
		return errors.New("state directory is already in use")
	}
	defer lock.Unlock()
	if o.init {
		if e = o.prepareInit(absData, absKey); e != nil {
			return e
		}
	}
	v, e := vault.Open(absKey)
	if e != nil {
		return errors.New("cannot load master key; initialize or restore its protected file")
	}
	sessionPath := filepath.Join(absData, "google-session.enc")
	if o.updateCookies {
		return updateCookies(v, sessionPath, input)
	}
	var owner *ownerCredentialFile
	if o.init {
		if owner, e = newOwnerCredentialFile(o.ownerFile); e != nil {
			return e
		}
		defer owner.cleanup()
	}
	s, e := store.Open(filepath.Join(absData, "smsbot.db"), v)
	if e != nil {
		return errors.New("cannot open state database")
	}
	defer s.DB.Close()
	switch {
	case o.archiveBackup != "" || o.archiveRestore != "":
		return o.runArchive(s, absData)
	case o.backup != "":
		return runBackup(s, o.backup)
	case o.restore != "":
		return runRestore(s, o.restore)
	case o.init:
		return owner.write(s)
	case o.pair:
		return pairGoogle(v, sessionPath, input)
	}
	return o.serve(s, v, sessionPath)
}

// prepareInit refuses to overwrite existing state and creates the master key if needed.
func (o *options) prepareInit(absData, absKey string) error {
	if o.ownerFile == "" {
		return errors.New("--owner-file is required for initialization")
	}
	if _, err := os.Stat(filepath.Join(absData, "smsbot.db")); err == nil {
		return errors.New("initialization requires a new state database")
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect initialization state")
	}
	if _, err := os.Stat(absKey); os.IsNotExist(err) {
		return vault.Initialize(absKey)
	} else if err != nil {
		return errors.New("cannot inspect master key")
	}
	return nil
}

func readBoundedCookies(input io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(b) > 65536 {
		return "", errors.New("cannot read bounded cookie input")
	}
	return string(b), nil
}

func updateCookies(v *vault.Vault, sessionPath string, input io.Reader) error {
	cookies, err := readBoundedCookies(input)
	if err != nil {
		return err
	}
	if err = transport.NewGoogle(v, sessionPath).UpdateCookies(cookies); err != nil {
		return err
	}
	fmt.Println("Cookies updated offline. Existing pairing preserved; encrypted backup saved beside the session. No Google requests made. Clear your sensitive clipboard locally. Start the service in paused mode to test authentication.")
	return nil
}

func pairGoogle(v *vault.Vault, sessionPath string, input io.Reader) error {
	cookies, err := readBoundedCookies(input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	g := transport.NewGoogle(v, sessionPath)
	if err = g.Pair(ctx, cookies, func(emoji string) { fmt.Println("Approve this emoji on your phone:", emoji) }); err != nil {
		return err
	}
	fmt.Println("Paired. Encrypted session saved. Clear your sensitive clipboard locally. Start the service in paused mode.")
	return nil
}

// ownerCredentialFile holds the new owner credential file open exclusively and
// removes it again unless the credential was saved.
type ownerCredentialFile struct {
	path  string
	file  *os.File
	saved bool
}

func newOwnerCredentialFile(path string) (*ownerCredentialFile, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("owner credential file must be new and writable")
	}
	return &ownerCredentialFile{path: path, file: f}, nil
}

func (c *ownerCredentialFile) cleanup() {
	c.file.Close()
	if !c.saved {
		os.Remove(c.path)
	}
}

func (c *ownerCredentialFile) write(s *store.Store) error {
	token := store.ID() + store.ID()
	if e := s.AddCredential(token, "Owner", "owner", 0); e != nil {
		return e
	}
	_, e := c.file.WriteString(token)
	if e == nil {
		e = c.file.Sync()
	}
	ce := c.file.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	c.saved = true
	fmt.Println("Initialized. Open the owner credential file locally; credentials are never printed.")
	return nil
}

func (o *options) runArchive(s *store.Store, absData string) error {
	manager := archive.New(s, filepath.Join(absData, "archive"), nil)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var counts archive.Counts
	if o.archiveBackup != "" {
		scope := archive.Scope{Since: o.archiveSince, Plaintext: o.archivePlain}
		if o.archiveConversation != "" {
			scope.Conversations = []string{o.archiveConversation}
		}
		output, err := os.OpenFile(o.archiveBackup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("archive output must be new and writable")
		}
		complete := false
		defer func() {
			output.Close()
			if !complete {
				os.Remove(o.archiveBackup)
			}
		}()
		counts, err = manager.Export(ctx, output, scope, nil)
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err != nil || closeErr != nil {
			return errors.New("archive export failed")
		}
		complete = true
	} else {
		in, err := os.Open(o.archiveRestore)
		if err != nil {
			return errors.New("cannot open archive")
		}
		defer in.Close()
		counts, err = manager.Restore(ctx, in, nil)
		if err != nil {
			return errors.New("archive restore failed; use fresh initialized state and original protected key")
		}
	}
	fmt.Printf("Archive operation complete: %d messages, %d originals, %d missing originals. Collection and sending remain disabled after restore.\n", counts.Messages, counts.SavedFiles, counts.MissingFiles)
	return nil
}

func runBackup(s *store.Store, path string) error {
	b, err := s.ExportBackup()
	if err != nil {
		return errors.New("backup failed")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("backup output must be new and writable")
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("backup write failed")
	}
	fmt.Println("Encrypted backup saved. Protect it and its original master key locally.")
	return nil
}

func runRestore(s *store.Store, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot read backup")
	}
	b, err := io.ReadAll(io.LimitReader(f, store.MaxBackupBytes+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(b) > store.MaxBackupBytes {
		return errors.New("cannot read bounded backup")
	}
	if err = s.RestoreBackup(b); err != nil {
		return errors.New("backup restore failed; use fresh initialized state and the original master key")
	}
	fmt.Println("Backup restored. Automation remains paused; provider and pairing credentials must be configured locally.")
	return nil
}

// serve runs the service: it always starts paused, connects the transport and
// runs the worker, capture, archive and history loops until interrupted.
func (o *options) serve(s *store.Store, v *vault.Vault, sessionPath string) error {
	origin, e := ValidateHosting(o.listen, o.origin, o.tlsCert, o.tlsKey)
	if e != nil {
		return e
	}
	var certificate tls.Certificate
	if o.tlsCert != "" {
		certificate, e = tls.LoadX509KeyPair(o.tlsCert, o.tlsKey)
		if e != nil {
			return errors.New("cannot load TLS certificate and key")
		}
	}
	if e = app.PrepareStore(s); e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var t transport.Transport = transport.NewFixture()
	var events app.EventSource
	if !o.fixture {
		google := transport.NewGoogle(v, sessionPath)
		google.Diagnostic = func(d transport.AuthDiagnostic) {
			b, err := json.Marshal(d)
			if err == nil {
				fmt.Fprintln(os.Stderr, "Authentication:", string(b))
			}
		}
		// One line per phone request (kind and purpose only), so phone wake-ups
		// can be reviewed later, across restarts.
		google.RequestLog = func(r transport.PhoneRequestLog) {
			b, err := json.Marshal(r)
			if err == nil {
				fmt.Fprintln(os.Stderr, "Phone request:", string(b))
			}
		}
		if e = google.Connect(ctx); e != nil {
			return e
		}
		t = google
		events = google
		defer google.Close()
	}
	service := app.New(s, t, filepath.Join(filepath.Dir(s.Path), "archive"))
	if e = service.Init(); e != nil {
		return e
	}
	workers := service.Start(ctx, events, func() {
		fmt.Fprintln(os.Stderr, "Worker: processing failed; automation paused; inspect activity locally")
	})
	defer workers.Stop()
	api := httpapi.New(service.Engine, service.Collection, origin, o.fixture)
	server := &http.Server{Addr: o.listen, Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	if o.tlsCert != "" {
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	errChan := make(chan error, 1)
	go func() {
		fmt.Println("SmsBot service:", origin, "(starts paused)")
		if o.tlsCert != "" {
			errChan <- server.ListenAndServeTLS("", "")
		} else {
			errChan <- server.ListenAndServe()
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errChan:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP service failed")
		}
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	return server.Shutdown(shutdown)
}

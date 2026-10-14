package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/gofrs/flock"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"smsbot-server/internal/bot"
	"smsbot-server/internal/httpapi"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"smsbot-server/internal/vault"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "SmsBot: operation failed:", e)
		os.Exit(1)
	}
}
func run() error {
	return runWithInput(os.Args[1:], os.Stdin)
}
func runWithInput(args []string, input io.Reader) error {
	flags := flag.NewFlagSet("smsbot", flag.ContinueOnError)
	data := flags.String("data", "", "state directory outside source")
	key := flags.String("key", "", "protected master key path outside state directory")
	listen := flags.String("listen", "127.0.0.1:8787", "loopback listen address")
	originFlag := flags.String("origin", "", "externally visible exact HTTPS origin for private access")
	tlsCert := flags.String("tls-cert", "", "PEM TLS certificate path")
	tlsKey := flags.String("tls-key", "", "PEM TLS private key path")
	backup := flags.String("backup", "", "write encrypted offline backup to a new file")
	restore := flags.String("restore", "", "restore encrypted backup into fresh initialized state")
	tokenFile := flags.String("owner-file", "", "file for initial owner credential")
	init := flags.Bool("init", false, "initialize encrypted state and owner credential")
	fixture := flags.Bool("fixture", false, "explicit offline fixture mode")
	pair := flags.Bool("pair", false, "pair Google Messages locally with owner emoji approval")
	updateCookies := flags.Bool("update-cookies", false, "replace cookies in the existing encrypted pairing offline")
	cookies := flags.Bool("cookies-stdin", false, "read Google cookies from stdin, never command arguments")
	if e := flags.Parse(args); e != nil {
		return e
	}
	modes := 0
	for _, selected := range []bool{*init, *fixture, *pair, *updateCookies, *backup != "", *restore != ""} {
		if selected {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("initialization, fixture, pairing and cookie-update modes are mutually exclusive")
	}
	if *cookies && !*pair && !*updateCookies {
		return errors.New("--cookies-stdin requires --pair or --update-cookies")
	}
	if (*pair || *updateCookies) && !*cookies {
		return errors.New("pairing and cookie updates require --cookies-stdin")
	}
	if *data == "" || *key == "" {
		return errors.New("--data and --key are required")
	}
	absData, e := filepath.Abs(*data)
	if e != nil {
		return e
	}
	absKey, e := filepath.Abs(*key)
	if e != nil {
		return e
	}
	rel, e := filepath.Rel(absData, absKey)
	if e != nil {
		return e
	}
	if rel == "." || (rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[:min(3, len(rel))] != ".."+string(os.PathSeparator)) {
		return errors.New("master key must be outside the state directory")
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
	if *init {
		if *tokenFile == "" {
			return errors.New("--owner-file is required for initialization")
		}
		if _, err := os.Stat(filepath.Join(absData, "smsbot.db")); err == nil {
			return errors.New("initialization requires a new state database")
		} else if !os.IsNotExist(err) {
			return errors.New("cannot inspect initialization state")
		}
		if _, err := os.Stat(absKey); os.IsNotExist(err) {
			if e = vault.Initialize(absKey); e != nil {
				return e
			}
		} else if err != nil {
			return errors.New("cannot inspect master key")
		}
	}
	v, e := vault.Open(absKey)
	if e != nil {
		return errors.New("cannot load master key; initialize or restore its protected file")
	}
	if *updateCookies {
		b, err := io.ReadAll(io.LimitReader(input, 65537))
		if err != nil || len(b) > 65536 {
			return errors.New("cannot read bounded cookie input")
		}
		g := transport.NewGoogle(v, filepath.Join(absData, "google-session.enc"))
		if err = g.UpdateCookies(string(b)); err != nil {
			return err
		}
		fmt.Println("Cookies updated offline. Existing pairing preserved; encrypted backup saved beside the session. No Google requests made. Clear your sensitive clipboard locally. Start the service in paused mode to test authentication.")
		return nil
	}
	var ownerOutput *os.File
	ownerSaved := false
	if *init {
		ownerOutput, e = os.OpenFile(*tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return errors.New("owner credential file must be new and writable")
		}
		defer func() {
			ownerOutput.Close()
			if !ownerSaved {
				os.Remove(*tokenFile)
			}
		}()
	}
	s, e := store.Open(filepath.Join(absData, "smsbot.db"), v)
	if e != nil {
		return errors.New("cannot open state database")
	}
	defer s.DB.Close()
	if *backup != "" {
		b, err := s.ExportBackup()
		if err != nil {
			return errors.New("backup failed")
		}
		f, err := os.OpenFile(*backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
	if *restore != "" {
		f, err := os.Open(*restore)
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
	if *init {
		token := store.ID() + store.ID()
		if e = s.AddCredential(token, "Owner", "owner", 0); e != nil {
			return e
		}
		f := ownerOutput
		_, e = f.WriteString(token)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		ownerSaved = true
		fmt.Println("Initialized. Open the owner credential file locally; credentials are never printed.")
		return nil
	}
	if *pair {
		b, err := io.ReadAll(io.LimitReader(input, 65537))
		if err != nil || len(b) > 65536 {
			return errors.New("cannot read bounded cookie input")
		}
		g := transport.NewGoogle(v, filepath.Join(absData, "google-session.enc"))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err = g.Pair(ctx, string(b), func(emoji string) { fmt.Println("Approve this emoji on your phone:", emoji) }); err != nil {
			return err
		}
		fmt.Println("Paired. Encrypted session saved. Clear your sensitive clipboard locally. Start the service in paused mode.")
		return nil
	}
	origin, e := ValidateHosting(*listen, *originFlag, *tlsCert, *tlsKey)
	if e != nil {
		return e
	}
	var certificate tls.Certificate
	if *tlsCert != "" {
		certificate, e = tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if e != nil {
			return errors.New("cannot load TLS certificate and key")
		}
	}
	if e = s.Recover(); e != nil {
		return e
	}
	if e = s.Pause(true); e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var t transport.Transport = transport.NewFixture()
	var google *transport.Google
	if !*fixture {
		google = transport.NewGoogle(v, filepath.Join(absData, "google-session.enc"))
		google.Diagnostic = func(d transport.AuthDiagnostic) {
			b, err := json.Marshal(d)
			if err == nil {
				fmt.Fprintln(os.Stderr, "Authentication:", string(b))
			}
		}
		if e = google.Connect(ctx); e != nil {
			return e
		}
		t = google
		defer google.Close()
	}
	engine := bot.New(s, t)
	cfg, e := engine.Configuration()
	if e != nil {
		return e
	}
	if _, e = s.Retention(cfg.Settings.RetentionDays); e != nil {
		return e
	}
	adapterDone := make(chan struct{})
	captureDone := make(chan struct{})
	go func() { defer close(captureDone); engine.RunCapture(ctx) }()
	if google != nil {
		go func() {
			defer close(adapterDone)
			google.Run(ctx, func(m transport.Incoming) {
				if _, err := engine.Ingest(ctx, m); err != nil {
					_ = engine.Pause(true)
				}
			}, func() { _ = engine.Pause(true) }, func(conv, txn, state, reported string) {
				if engine.Reconcile(conv, txn, state, reported) != nil {
					_ = engine.Pause(true)
				}
			})
		}()
	} else {
		close(adapterDone)
	}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		engine.Run(ctx, func(error) {
			_ = engine.Pause(true)
			fmt.Fprintln(os.Stderr, "Worker: processing failed; automation paused; inspect activity locally")
		})
	}()
	api := httpapi.New(engine, origin, *fixture)
	server := &http.Server{Addr: *listen, Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	if *tlsCert != "" {
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	errChan := make(chan error, 1)
	go func() {
		fmt.Println("SmsBot service:", origin, "(starts paused)")
		if *tlsCert != "" {
			errChan <- server.ListenAndServeTLS("", "")
		} else {
			errChan <- server.ListenAndServe()
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errChan:
		if !errors.Is(err, http.ErrServerClosed) {
			cancel()
			<-workerDone
			<-adapterDone
			<-captureDone
			return errors.New("HTTP service failed")
		}
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	e = server.Shutdown(shutdown)
	<-workerDone
	<-adapterDone
	<-captureDone
	return e
}

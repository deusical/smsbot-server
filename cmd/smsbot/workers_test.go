package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/bot"
)

func TestWorkerPoolFlags(t *testing.T) {
	t.Parallel()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(strings.Repeat("t", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	local := &bot.HTTPDownloader{Endpoint: "http://127.0.0.1:8767/download", Token: strings.Repeat("l", 40)}
	pool, err := workerPool([]string{
		"lenovo=http://100.92.5.20:8767/download," + token,
		"desk=http://desk.tailddfcef.ts.net:8767/download," + token,
	}, local)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, w := range pool.Workers {
		names = append(names, w.Name)
	}
	if strings.Join(names, ",") != "lenovo,desk,local" || pool.Workers[2].HTTPDownloader != local || !pool.Workers[0].Remote {
		t.Fatal("pool", names)
	}
	for _, bad := range [][]string{
		{"lenovo=http://192.168.1.20:8767/download," + token},             // LAN, not Tailscale
		{"lenovo=http://100.92.5.20:8767/download"},                       // no token file
		{"lenovo=http://100.92.5.20:8767/download," + token + ".missing"}, // unreadable token
		{"http://100.92.5.20:8767/download," + token},                     // no name
		{"a=http://100.92.5.20/download," + token, "a=http://100.92.5.21/download," + token},
	} {
		if _, err := workerPool(bad, local); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	// Without a local worker the pool is only the remote ones.
	if pool, err := workerPool([]string{"lenovo=http://100.92.5.20:8767/download," + token}, nil); err != nil || len(pool.Workers) != 1 {
		t.Fatal(pool, err)
	}
}

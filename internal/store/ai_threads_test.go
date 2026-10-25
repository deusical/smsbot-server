package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func aiPlan(thread, parent, prompt string) Plan {
	return Plan{Participant: "fixture-peer", Input: prompt, Memory: true, MemoryHours: 24, Model: "fixture/model", Thread: &AIThreadPlan{ID: thread, Parent: parent, RetentionDays: 7}}
}
func enqueueAI(t *testing.T, s *Store, id string, p Plan) {
	t.Helper()
	ok, err := s.Enqueue(id, "fixture-chat", "!ask", p)
	if err != nil || !ok {
		t.Fatalf("enqueue %s: %t %v", id, ok, err)
	}
}
func answerAI(t *testing.T, s *Store, id, wire string, p Plan, now time.Time) {
	t.Helper()
	p.Reply = "answer " + id
	txn := "txn-" + id
	if err := s.SubmittingPlan(id, txn, p, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAIAnswer("fixture-chat", txn, wire, now, 24, 7); err != nil {
		t.Fatal(err)
	}
}

func TestAIThreadsBranchPersistenceAndEncryption(t *testing.T) {
	s := inboxStore(t)
	if err := s.Enroll("fixture-chat", "fixture-peer"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	root := aiPlan("thread-a", "", "private synthetic root")
	enqueueAI(t, s, "a", root)
	if p, err := s.FindAIParent("fixture-chat", "wire-a"); err != nil || p != nil {
		t.Fatal("unsent answer was replyable", err)
	}
	answerAI(t, s, "a", "wire-a", root, now)
	for _, id := range []string{"b", "c"} {
		p := aiPlan("thread-a", "a", "prompt "+id)
		enqueueAI(t, s, id, p)
		answerAI(t, s, id, "wire-"+id, p, now)
	}
	parent, err := s.FindAIParent("fixture-chat", "wire-b")
	if err != nil || parent == nil {
		t.Fatal(err)
	}
	turns, omitted, err := s.AIHistory(*parent, "fixture-chat", "fixture-peer", "!ask", 0, now, 24, 7, 8192)
	if err != nil || omitted || len(turns) != 4 || turns[0].Content != "private synthetic root" || turns[2].Content != "prompt b" {
		t.Fatalf("branch context: %#v %v", turns, err)
	}
	if _, _, err = s.AIHistory(*parent, "fixture-chat", "other-peer", "!ask", 0, now, 24, 7, 8192); !errors.Is(err, ErrAIThreadUnavailable) {
		t.Fatal("wrong peer got context")
	}
	if p, err := s.FindAIParent("another-chat", "wire-b"); err != nil || p != nil {
		t.Fatal("cross-chat answer resolved", err)
	}
	// A tiny budget omits whole exchanges, never half of a prompt/answer pair.
	turns, omitted, err = s.AIHistory(*parent, "fixture-chat", "fixture-peer", "!ask", 0, now, 24, 7, 1)
	if err != nil || !omitted || len(turns) != 0 {
		t.Fatal("context was split", err)
	}
	path, v := s.Path, s.Vault
	if err = s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		b, _ := os.ReadFile(path + suffix)
		if bytes.Contains(b, []byte("private synthetic root")) || bytes.Contains(b, []byte("answer a")) {
			t.Fatal("plaintext thread content")
		}
	}
	restored, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.DB.Close()
	p, err := restored.FindAIParent("fixture-chat", "wire-b")
	if err != nil || p == nil {
		t.Fatal("restart lost reference", err)
	}
	turns, _, err = restored.AIHistory(*p, "fixture-chat", "fixture-peer", "!ask", 0, now, 24, 7, 8192)
	if err != nil || len(turns) != 4 {
		t.Fatal("restart lost ancestry", err)
	}
}

func TestAIThreadsAtomicAcceptanceResetAndExpiry(t *testing.T) {
	s := inboxStore(t)
	s.Enroll("fixture-chat", "fixture-peer")
	now := time.Now()
	p := aiPlan("root", "", "prompt")
	enqueueAI(t, s, "a", p)
	duplicate := aiPlan("would-be-orphan", "", "different")
	if ok, err := s.Enqueue("a", "fixture-chat", "!ask", duplicate); err != nil || ok {
		t.Fatal("duplicate accepted", err)
	}
	var count int
	s.DB.QueryRow("SELECT count(*) FROM ai_threads").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate orphaned thread")
	}
	bad := aiPlan("root", "missing-parent", "bad")
	if _, err := s.Enqueue("bad", "fixture-chat", "!ask", bad); err == nil {
		t.Fatal("invalid parent accepted")
	}
	if found, _ := s.JobExists("bad"); found {
		t.Fatal("failed enqueue left a job")
	}
	answerAI(t, s, "a", "wire", p, now)
	parent, _ := s.FindAIParent("fixture-chat", "wire")
	if _, _, err := s.AIHistory(*parent, "fixture-chat", "fixture-peer", "!ask", 0, now.Add(2*time.Hour), 1, 7, 8192); !errors.Is(err, ErrAIThreadUnavailable) {
		t.Fatal("shorter memory policy ignored")
	}
	child := aiPlan("root", "a", "queued child")
	enqueueAI(t, s, "b", child)
	if err := s.ClearMemory("fixture-chat"); err != nil {
		t.Fatal(err)
	}
	if ref, err := s.FindAIParent("fixture-chat", "wire"); err != nil || ref != nil {
		t.Fatal("clear retained answer", err)
	}
	var state, plan string
	s.DB.QueryRow("SELECT state,plan FROM jobs WHERE id='b'").Scan(&state, &plan)
	if state != "cancelled" || plan != "" {
		t.Fatal("clear left queued context")
	}
	if err := s.CheckAIThread("fixture-chat", "fixture-peer", "!ask", child, now, 24, 7); !errors.Is(err, ErrAIThreadUnavailable) {
		t.Fatal("deleted thread valid")
	}
	s.DB.QueryRow("SELECT count(*) FROM ai_exchanges").Scan(&count)
	if count != 0 {
		t.Fatal("clear did not cascade")
	}
	// Full expiry cascades branches and references, independently of messages.
	p = aiPlan("expire", "", "later")
	p.MemoryEpoch = 1
	enqueueAI(t, s, "e", p)
	answerAI(t, s, "e", "wire-e", p, now)
	pending := aiPlan("expire", "e", "pending context")
	pending.MemoryEpoch = 1
	enqueueAI(t, s, "f", pending)
	if err := s.PruneAIThreads(now.Add(25*time.Hour), 24, 7); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT count(*) FROM ai_threads").Scan(&count)
	if count != 0 {
		t.Fatal("expiry retained thread")
	}
	s.DB.QueryRow("SELECT state,plan FROM jobs WHERE id='f'").Scan(&state, &plan)
	if state != "cancelled" || plan != "" {
		t.Fatal("expiry left queued context snapshot")
	}
}

func TestAIAnswerProofUncertaintyAndNoEchoLifetimeExtension(t *testing.T) {
	s := inboxStore(t)
	s.Enroll("fixture-chat", "fixture-peer")
	now := time.Now()
	p := aiPlan("root", "", "prompt")
	enqueueAI(t, s, "a", p)
	if err := s.RecordAIAnswer("fixture-chat", "txn-a", "wire", now, 24, 7); err != nil {
		t.Fatal(err)
	}
	if ref, _ := s.FindAIParent("fixture-chat", "wire"); ref != nil {
		t.Fatal("unprepared answer trusted")
	}
	p.Reply = "actual answer"
	if err := s.SubmittingPlan("a", "txn-a", p, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete("a", "unknown_submission", "timeout"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAIAnswer("wrong-chat", "txn-a", "wire", now, 24, 7); err != nil {
		t.Fatal(err)
	}
	if ref, _ := s.FindAIParent("fixture-chat", "wire"); ref != nil {
		t.Fatal("wrong chat proof accepted")
	}
	if err := s.RecordAIAnswer("fixture-chat", "txn-a", "wire", now, 24, 7); err != nil {
		t.Fatal(err)
	}
	var expires string
	s.DB.QueryRow("SELECT expires_at FROM ai_threads").Scan(&expires)
	if err := s.RecordAIAnswer("fixture-chat", "txn-a", "wire", now.Add(time.Hour), 24, 7); err != nil {
		t.Fatal(err)
	}
	var after string
	s.DB.QueryRow("SELECT expires_at FROM ai_threads").Scan(&after)
	if after != expires {
		t.Fatal("echo extended lifetime")
	}
	var state string
	s.DB.QueryRow("SELECT state FROM jobs WHERE id='a'").Scan(&state)
	if state != "submitted" {
		t.Fatal("exact evidence did not reconcile unknown send")
	}
	// A second exchange cannot steal an existing upstream answer ID.
	child := aiPlan("root", "a", "second")
	enqueueAI(t, s, "b", child)
	child.Reply = "another"
	if err := s.SubmittingPlan("b", "txn-b", child, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAIAnswer("fixture-chat", "txn-b", "wire", now, 24, 7); err == nil {
		t.Fatal("conflicting alias overwritten")
	}
	ref, _ := s.FindAIParent("fixture-chat", "wire")
	if ref == nil || ref.Exchange != "a" {
		t.Fatal("conflict changed original mapping")
	}
}

func TestAIThreadBackupExcludesAncestryAndRestoresNoTriggers(t *testing.T) {
	s := inboxStore(t)
	s.Enroll("fixture-chat", "fixture-peer")
	p := aiPlan("root", "", "prompt")
	p.Context = []Turn{{Role: "user", Content: "private context snapshot"}}
	enqueueAI(t, s, "a", p)
	data, err := s.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.Vault.Open("backup", string(data))
	if err != nil {
		t.Fatal(err)
	}
	var b backupSnapshot
	if err = json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Jobs) != 1 || b.Jobs[0].Plan != "" {
		t.Fatal("thread plan exported")
	}
	dest := inboxStore(t)
	if err = dest.RestoreBackup(data); err != nil {
		t.Fatal(err)
	}
	var count int
	dest.DB.QueryRow("SELECT count(*) FROM ai_threads").Scan(&count)
	if count != 0 {
		t.Fatal("restore recreated AI context")
	}
}

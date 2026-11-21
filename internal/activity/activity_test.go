package activity

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTapCopiesAgentRecords(t *testing.T) {
	var out bytes.Buffer
	l := New()
	log := slog.New(Tap(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}), l))
	log.Info("tunnel: rejected", "agent", "ag_one", "err", "invalid boot token")
	log.Debug("harness stderr", "agent", "ag_one", "line", "[session/create] phase=settings")
	log.With("component", "acp", "agent", "ag_two").Info("connection closed", "took", 1500*time.Millisecond)
	log.Info("unrelated", "user", "usr_x")
	one := l.Since("ag_one", 0)
	if len(one) != 2 || one[0].Source != "tunnel" || !strings.Contains(one[0].Message, `err="invalid boot token"`) || one[1].Level != "debug" || one[1].Source != "stderr" {
		t.Fatalf("ag_one: %+v", one)
	}
	two := l.Since("ag_two", 0)
	if len(two) != 1 || two[0].Source != "acp" || !strings.Contains(two[0].Message, "took=1.5s") {
		t.Fatalf("ag_two: %+v", two)
	}
	if strings.Contains(out.String(), "harness stderr") {
		t.Fatal("a debug record reached the info-level server log")
	}
	if !strings.Contains(out.String(), "unrelated") {
		t.Fatal("records still reach the server log")
	}
	if got := l.Since("ag_one", one[0].Seq); len(got) != 1 || got[0].Seq != one[1].Seq {
		t.Fatalf("since: %+v", got)
	}
}

func TestLogIsBounded(t *testing.T) {
	l := New()
	for i := 0; i < Keep+50; i++ {
		l.Add("ag_a", slog.LevelInfo, "x", strings.Repeat("y", maxMessage+10))
	}
	es := l.Since("ag_a", 0)
	if len(es) != Keep || es[0].Seq != 51 || len(es[0].Message) > maxMessage+len("…") {
		t.Fatalf("kept %d, first seq %d, len %d", len(es), es[0].Seq, len(es[0].Message))
	}
	l.Forget("ag_a")
	if len(l.Since("ag_a", 0)) != 0 {
		t.Fatal("forget")
	}
}

package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/manager/api"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type captureHistorySQL struct {
	logger.Interface
	query string
}

func (l *captureHistorySQL) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	l.query, _ = fc()
}
func TestHistoryRolloutIsIncludedInSQLWrite(t *testing.T) {
	log := &captureHistorySQL{Interface: logger.Default}
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "test:test@tcp(127.0.0.1:1)/test", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	s := &sqlStore{namespace: "namespace"}
	after := &record{Thread: api.Thread{ID: "thread"}, History: api.History{Version: 1, Rollout: []byte(`[{"Seq":1}]`)}}
	if err = s.saveContent(db, nil, after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.query, "`rollout`") || !strings.Contains(log.query, `[{"Seq":1}]`) {
		t.Fatalf("rollout omitted from SQL: %s", log.query)
	}
}
func TestHistoryRejectsInvalidRolloutWithoutAdvancingVersion(t *testing.T) {
	m, thread, _ := setup(t)
	claim, err := m.ClaimThread(ctx, thread.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.SaveHistory(ctx, claim.Permit, api.History{Rollout: []byte(`invalid`)}); err == nil {
		t.Fatal("invalid rollout accepted")
	}
	h, err := m.LoadHistory(ctx, thread.ID)
	if err != nil || h.Version != 0 {
		t.Fatalf("history=%+v err=%v", h, err)
	}
}

func TestHistoryRolloutSharesVersionAndPermitFence(t *testing.T) {
	m, thread, _ := setup(t)
	claim, err := m.ClaimThread(ctx, thread.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := m.SaveHistory(ctx, claim.Permit, api.History{Messages: []byte(`[]`), Rollout: []byte(`[{"Seq":1}]`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.SaveHistory(ctx, claim.Permit, api.History{Rollout: []byte(`[]`)}); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("stale version accepted: %v", err)
	}
	stale := claim.Permit
	stale.Token = "wrong"
	saved.Rollout = []byte(`[]`)
	if _, err = m.SaveHistory(ctx, stale, saved); !errors.Is(err, api.ErrPermitLost) {
		t.Fatalf("stale permit accepted: %v", err)
	}
	loaded, err := m.LoadHistory(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 1 || string(loaded.Rollout) != `[{"Seq":1}]` {
		t.Fatalf("rollout overwritten: %+v", loaded)
	}
}

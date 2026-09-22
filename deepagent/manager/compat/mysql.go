package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	sqldriver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Config identifies shared stores. Namespace is part of every database and Redis key.
type Config struct {
	Namespace, MySQLDSN, RedisAddr, RedisPassword string
	RedisDB                                       int
}
type Manager struct{ *engine }

var _ api.Manager = (*Manager)(nil)

type threadRow struct {
	HasHistory  bool      `gorm:"index"`
	Namespace   string    `gorm:"primaryKey;type:varbinary(191)"`
	ID          string    `gorm:"primaryKey;type:varbinary(64)"`
	SessionID   string    `gorm:"type:varbinary(191);index:idx_da_session,priority:2"`
	State       string    `gorm:"size:16;index"`
	PermitUntil time.Time `gorm:"precision:6;index"`
	CreatedAt   time.Time `gorm:"precision:6"`
	Body        []byte    `gorm:"type:longblob"`
}

func (threadRow) TableName() string { return "deepagent_threads" }

type namespaceRow struct {
	Namespace string `gorm:"primaryKey;type:varbinary(191)"`
	Sequence  int64
}

func (namespaceRow) TableName() string { return "deepagent_namespaces" }

type eventRow struct {
	Namespace string `gorm:"primaryKey;type:varbinary(191);uniqueIndex:idx_da_event_id,priority:1;index:idx_da_event_session,priority:1;index:idx_da_event_thread,priority:1"`
	Sequence  int64  `gorm:"primaryKey;autoIncrement:false;index:idx_da_event_session,priority:3;index:idx_da_event_thread,priority:3"`
	EventID   string `gorm:"type:varbinary(64);uniqueIndex:idx_da_event_id,priority:2"`
	ThreadID  string `gorm:"type:varbinary(64);index:idx_da_event_thread,priority:2"`
	SessionID string `gorm:"type:varbinary(191);index:idx_da_event_session,priority:2"`
	Body      []byte `gorm:"type:longblob"`
}

func (eventRow) TableName() string { return "deepagent_events" }

type checkpointRow struct {
	Namespace string `gorm:"primaryKey;type:varbinary(191)"`
	Key       string `gorm:"primaryKey;type:varbinary(191)"`
	Body      []byte `gorm:"type:longblob"`
}

func (checkpointRow) TableName() string { return "deepagent_checkpoints" }

type sqlStore struct {
	db                *gorm.DB
	redis             *redis.Client
	namespace, prefix string
	stop              context.CancelFunc
	ctx               context.Context
	once              sync.Once
	closeErr          error
}

func (s *sqlStore) pendingKey() string  { return s.prefix + "pending" }
func (s *sqlStore) completeKey() string { return s.prefix + "complete" }
func (s *sqlStore) enqueueInput(ctx context.Context, inputID string) error {
	// The sequence is allocated by Redis; ZSET ordering is stable across workers.
	score, err := s.redis.Incr(ctx, s.prefix+"message-seq").Result()
	if err != nil {
		return err
	}
	pipe := s.redis.TxPipeline()
	pipe.HSet(ctx, s.prefix+"message-score", inputID, score)
	pipe.ZAdd(ctx, s.pendingKey(), redis.Z{Score: float64(score), Member: inputID})
	_, err = pipe.Exec(ctx)
	return err
}
func (s *sqlStore) completeInput(ctx context.Context, inputID string) error {
	score, err := s.redis.HGet(ctx, s.prefix+"message-score", inputID).Float64()
	if err == redis.Nil {
		score = float64(time.Now().UnixNano())
	} else if err != nil {
		return err
	}
	pipe := s.redis.TxPipeline()
	pipe.ZRem(ctx, s.pendingKey(), inputID)
	pipe.ZAdd(ctx, s.completeKey(), redis.Z{Score: score, Member: inputID})
	_, err = pipe.Exec(ctx)
	return err
}
func (s *sqlStore) requeueInput(ctx context.Context, inputID string) error {
	score, err := s.redis.HGet(ctx, s.prefix+"message-score", inputID).Float64()
	if err == redis.Nil {
		score = float64(time.Now().UnixNano())
	} else if err != nil {
		return err
	}
	return s.redis.ZAdd(ctx, s.pendingKey(), redis.Z{Score: score, Member: inputID}).Err()
}

func New(ctx context.Context, c Config) (*Manager, error) {
	if strings.TrimSpace(c.Namespace) == "" || len(c.Namespace) > 191 || c.MySQLDSN == "" || c.RedisAddr == "" {
		return nil, fmt.Errorf("namespace (1-191 bytes), MySQLDSN and RedisAddr required")
	}
	dc, e := sqldriver.ParseDSN(c.MySQLDSN)
	if e != nil {
		return nil, fmt.Errorf("mysql DSN: %w", e)
	}
	dc.ParseTime = true
	dc.Loc = time.UTC
	db, e := gorm.Open(mysql.Open(dc.FormatDSN()), &gorm.Config{DisableAutomaticPing: true, Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{SlowThreshold: 200 * time.Millisecond, LogLevel: logger.Warn, IgnoreRecordNotFoundError: true, ParameterizedQueries: true})})
	if e != nil {
		return nil, e
	}
	sqlDB, e := db.DB()
	if e != nil {
		return nil, e
	}
	rc := redis.NewClient(&redis.Options{Addr: c.RedisAddr, Password: c.RedisPassword, DB: c.RedisDB})
	life, stop := context.WithCancel(context.Background())
	s := &sqlStore{db: db, redis: rc, namespace: c.Namespace, prefix: "deepagent:" + keyPart(c.Namespace) + ":", ctx: life, stop: stop}
	fail := func(e error) (*Manager, error) { _ = s.close(); return nil, e }
	if e = sqlDB.PingContext(ctx); e != nil {
		return fail(fmt.Errorf("mysql: %w", e))
	}
	if e = rc.Ping(ctx).Err(); e != nil {
		return fail(fmt.Errorf("redis: %w", e))
	}
	if e = migrateSchema(db.WithContext(ctx)); e != nil {
		return fail(e)
	}
	if e = db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&namespaceRow{Namespace: c.Namespace}).Error; e != nil {
		return fail(e)
	}
	return &Manager{&engine{namespace: c.Namespace, store: s}}, nil
}
func keyPart(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func dbNow(db *gorm.DB) (time.Time, error) {
	var micros int64
	if e := db.Raw("SELECT CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(6)) * 1000000 AS SIGNED)").Scan(&micros).Error; e != nil {
		return time.Time{}, e
	}
	return time.UnixMicro(micros).UTC(), nil
}
func decode(row threadRow) (*record, error) {
	var r record
	if e := json.Unmarshal(row.Body, &r); e != nil {
		return nil, fmt.Errorf("decode thread %s: %w", row.ID, e)
	}
	r.legacy = len(r.Inputs) > 0 || r.History.Version > 0
	r.hasHistory = row.HasHistory
	return &r, nil
}
func encode(r *record) (threadRow, error) {
	small := *r
	small.Inputs = nil
	small.History = api.History{}
	b, e := json.Marshal(&small)
	hasHistory := nonemptyHistory(r.History)
	if r.History.Version == 0 {
		hasHistory = r.hasHistory
	}
	until := r.Permit.ExpiresAt
	if until.IsZero() {
		until = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return threadRow{HasHistory: hasHistory, Namespace: r.Thread.Namespace, ID: r.Thread.ID, SessionID: r.Thread.SessionID, State: string(r.Thread.State), PermitUntil: until, CreatedAt: r.Thread.CreatedAt, Body: b}, e
}
func mapError(e error) error {
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return api.ErrNotFound
	}
	return e
}
func (s *sqlStore) create(ctx context.Context, r *record) error {
	r.Revision = 1
	row, e := encode(r)
	if e != nil {
		return e
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&row).Error; e != nil {
			return e
		}
		return s.saveContent(tx, nil, r)
	})
}
func (s *sqlStore) loadPermit(ctx context.Context, id string) (*record, error) {
	return s.readScheduling(s.db.WithContext(ctx), id)
}
func (s *sqlStore) readScheduling(db *gorm.DB, id string) (*record, error) {
	var row threadRow
	if e := db.Where("namespace = ? AND id = ?", s.namespace, id).Take(&row).Error; e != nil {
		return nil, mapError(e)
	}
	r, e := decode(row)
	if e != nil {
		return nil, e
	}
	r.now, e = dbNow(db)
	return r, e
}
func (s *sqlStore) load(ctx context.Context, id string) (*record, error) {
	var r *record
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		r, e = s.readScheduling(tx, id)
		if e != nil {
			return e
		}
		return s.loadContent(tx, r)
	})
	return r, e
}
func (s *sqlStore) update(ctx context.Context, id string, fn func(*record) error) (*record, error) {
	return s.mutate(ctx, id, true, fn)
}
func (s *sqlStore) mutate(ctx context.Context, id string, withContent bool, fn func(*record) error) (*record, error) {
	var result *record
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row threadRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("namespace = ? AND id = ?", s.namespace, id).Take(&row).Error; e != nil {
			return mapError(e)
		}
		r, e := decode(row)
		if e != nil {
			return e
		}
		r.now, e = dbNow(tx)
		if e != nil {
			return e
		}
		hydrate := withContent || r.legacy
		if hydrate {
			if e = s.loadContent(tx, r); e != nil {
				return e
			}
		}
		before := cloneRecord(r)
		before.legacy = r.legacy
		if e = fn(r); e != nil {
			return e
		}
		for _, event := range recordEvents(r) {
			if event.Durable() {
				if e = s.appendEvent(tx, event); e != nil {
					return e
				}
			}
		}
		if hydrate {
			if e = s.saveContent(tx, before, r); e != nil {
				return e
			}
		}
		r.Revision++
		r.Thread.UpdatedAt = r.now
		row, e = encode(r)
		if e != nil {
			return e
		}
		if e = tx.Model(&threadRow{}).Where("namespace = ? AND id = ?", s.namespace, id).Updates(map[string]any{"state": row.State, "permit_until": row.PermitUntil, "has_history": row.HasHistory, "body": row.Body}).Error; e != nil {
			return e
		}
		result = r
		return nil
	})
	return result, e
}
func (s *sqlStore) appendEvent(tx *gorm.DB, event *protocol.Event) error {
	// Serialize sequence allocation through commit; auto-increment alone permits a
	// later sequence to commit first and makes a cursor permanently skip an event.
	var ns namespaceRow
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("namespace = ?", s.namespace).Take(&ns).Error; e != nil {
		return e
	}
	var existing eventRow
	e := tx.Where("namespace = ? AND event_id = ?", s.namespace, event.ID).Take(&existing).Error
	if e == nil {
		var stored protocol.Event
		if e = json.Unmarshal(existing.Body, &stored); e != nil {
			return e
		}
		if stored.ThreadID != event.ThreadID || !sameEvent(stored, *event) {
			return api.ErrConflict
		}
		*event = stored
		return nil
	}
	if !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	ns.Sequence++
	event.Sequence = ns.Sequence
	b, e := json.Marshal(event)
	if e != nil {
		return e
	}
	if e = tx.Model(&namespaceRow{}).Where("namespace = ?", s.namespace).Update("sequence", ns.Sequence).Error; e != nil {
		return e
	}
	return tx.Create(&eventRow{Namespace: s.namespace, Sequence: event.Sequence, EventID: event.ID, ThreadID: event.ThreadID, SessionID: event.SessionID, Body: b}).Error
}
func (s *sqlStore) list(ctx context.Context, session string, runnable bool, limit, offset int) ([]api.Thread, error) {
	limit, offset = bounds(limit, offset)
	q := s.db.WithContext(ctx).Where("namespace = ?", s.namespace)
	if session != "" {
		q = q.Where("session_id = ?", session)
	}
	if runnable {
		now, e := dbNow(q)
		if e != nil {
			return nil, e
		}
		q = q.Where("state IN ? AND permit_until <= ?", []string{string(api.Ready), string(api.Running), string(api.Closing)}, now)
	}
	var rows []threadRow
	if e := q.Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]api.Thread, 0, len(rows))
	for _, row := range rows {
		r, e := decode(row)
		if e != nil {
			return nil, e
		}
		out = append(out, r.Thread)
	}
	return out, nil
}
func (s *sqlStore) events(ctx context.Context, f api.EventFilter) ([]protocol.Event, error) {
	limit, _ := bounds(f.Limit, 0)
	q := s.db.WithContext(ctx).Where("namespace = ? AND sequence > ?", s.namespace, f.After)
	if f.ThreadID != "" {
		q = q.Where("thread_id = ?", f.ThreadID)
	}
	if f.SessionID != "" {
		q = q.Where("session_id = ?", f.SessionID)
	}
	var rows []eventRow
	if e := q.Order("sequence ASC").Limit(limit).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]protocol.Event, 0, len(rows))
	for _, row := range rows {
		var event protocol.Event
		if e := json.Unmarshal(row.Body, &event); e != nil {
			return nil, e
		}
		out = append(out, event)
	}
	return out, nil
}
func (s *sqlStore) deliver(ctx context.Context, r *record) ([]protocol.Input, error) {
	// Redis snapshots are immutable and addressed by SQL revision. Delayed writes
	// from an old owner cannot overwrite a new owner's queue. SQL is the durable
	// receipt journal and repairs cache eviction or a failed cross-store write.
	key := fmt.Sprintf("%squeue:%s:%d", s.prefix, keyPart(r.Thread.ID), r.Revision)
	cached, e := s.redis.Get(ctx, key).Bytes()
	if e == nil {
		var inputs []protocol.Input
		if json.Unmarshal(cached, &inputs) == nil {
			return inputs, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	inputs := pending(r)
	b, e := json.Marshal(inputs)
	if e != nil {
		return nil, e
	}
	_ = s.redis.Set(ctx, key, b, 5*time.Minute).Err()
	return inputs, nil
}
func (s *sqlStore) publish(ctx context.Context, e protocol.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.redis.Publish(ctx, s.prefix+"events:"+keyPart(e.SessionID), b).Err()
}
func (s *sqlStore) subscribe(ctx context.Context, session string) (*api.Subscription, error) {
	life, cancel := context.WithCancel(ctx)
	ps := s.redis.Subscribe(life, s.prefix+"events:"+keyPart(session))
	if _, e := ps.Receive(life); e != nil {
		cancel()
		_ = ps.Close()
		return nil, e
	}
	events := make(chan protocol.Event, 128)
	errs := make(chan error, 1)
	var once sync.Once
	closeFn := func() { once.Do(func() { cancel(); _ = ps.Close() }) }
	go func() {
		select {
		case <-s.ctx.Done():
			closeFn()
		case <-life.Done():
		}
	}()
	go func() {
		defer close(events)
		defer close(errs)
		defer closeFn()
		for {
			msg, e := ps.ReceiveMessage(life)
			if e != nil {
				if life.Err() == nil {
					select {
					case errs <- e:
					default:
					}
				}
				return
			}
			var event protocol.Event
			if e = json.Unmarshal([]byte(msg.Payload), &event); e != nil {
				select {
				case errs <- e:
				default:
				}
				continue
			}
			if event.Namespace != s.namespace || event.SessionID != session {
				continue
			}
			select {
			case events <- event:
			case <-life.Done():
				return
			}
		}
	}()
	return &api.Subscription{Events: events, Errors: errs, Close: closeFn}, nil
}
func (s *sqlStore) checkpoint(ctx context.Context, key string) ([]byte, error) {
	var row checkpointRow
	if e := s.db.WithContext(ctx).Where("namespace = ? AND `key` = ?", s.namespace, key).Take(&row).Error; e != nil {
		return nil, mapError(e)
	}
	return row.Body, nil
}
func (s *sqlStore) putCheckpoint(ctx context.Context, key string, b []byte) error {
	if len(key) > 191 {
		return fmt.Errorf("checkpoint key exceeds 191 bytes")
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "namespace"}, {Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"body"})}).Create(&checkpointRow{Namespace: s.namespace, Key: key, Body: b}).Error
}
func (s *sqlStore) close() error {
	s.once.Do(func() {
		s.stop()
		e1 := s.redis.Close()
		db, e2 := s.db.DB()
		var e3 error
		if e2 == nil {
			e3 = db.Close()
		}
		s.closeErr = errors.Join(e1, e2, e3)
	})
	return s.closeErr
}

// GORM AutoMigrate intentionally avoids altering primary-key column types. An
// existing default-collation schema therefore needs this explicit conversion;
// otherwise logically distinct namespaces can share both data and permit rows.
func migrateSchema(db *gorm.DB) error {
	if err := db.AutoMigrate(&threadRow{}, &namespaceRow{}, &eventRow{}, &checkpointRow{}, &memoryRow{}, &inputRow{}, &historyRow{}); err != nil {
		return err
	}
	models := []struct {
		model any
		keys  map[string]string
	}{
		{&threadRow{}, map[string]string{"namespace": "Namespace", "id": "ID"}},
		{&namespaceRow{}, map[string]string{"namespace": "Namespace"}},
		{&eventRow{}, map[string]string{"namespace": "Namespace"}},
		{&checkpointRow{}, map[string]string{"namespace": "Namespace", "key": "Key"}},
		{&memoryRow{}, map[string]string{"namespace": "Namespace", "key": "Key"}},
		{&inputRow{}, map[string]string{"namespace": "Namespace", "thread_id": "ThreadID", "id": "ID"}},
		{&historyRow{}, map[string]string{"namespace": "Namespace", "thread_id": "ThreadID"}},
	}
	for _, m := range models {
		columns, err := db.Migrator().ColumnTypes(m.model)
		if err != nil {
			return err
		}
		for _, column := range columns {
			if field, ok := m.keys[column.Name()]; ok && !strings.EqualFold(column.DatabaseTypeName(), "varbinary") {
				if err := db.Migrator().AlterColumn(m.model, field); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *sqlStore) renewPermit(ctx context.Context, p api.Permit, ttl time.Duration) (api.Permit, error) {
	r, e := s.mutate(ctx, p.ThreadID, false, func(r *record) error {
		if e := validPermit(r, p); e != nil {
			return e
		}
		r.Permit.ExpiresAt = r.now.Add(ttl)
		return nil
	})
	if e != nil {
		return api.Permit{}, e
	}
	return r.Permit, nil
}

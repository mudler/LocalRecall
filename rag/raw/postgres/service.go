// Package postgres implements isolated, embedder-free raw PostgreSQL storage.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mudler/localrecall/rag/internal/pgutil"
	"github.com/mudler/localrecall/rag/raw"
	"os"
	"sync"
	"time"
)

type Config struct{ DatabaseURL string }
type Service struct {
	pool   *pgxpool.Pool
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
	stop   context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func New(ctx context.Context, cfg Config) (*Service, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pc, e := pgxpool.ParseConfig(cfg.DatabaseURL)
	if e != nil {
		return nil, dbError(e)
	}
	pgutil.ApplyPoolLimits(pc, os.Getenv)
	pgutil.ApplyConnTimeouts(pc, os.Getenv)
	pc.ConnConfig.RuntimeParams["lock_timeout"] = "5s"
	pc.ConnConfig.RuntimeParams["statement_timeout"] = "30s"
	p, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		return nil, dbError(e)
	}
	stop, c := context.WithCancel(context.Background())
	s := &Service{pool: p, stop: stop, cancel: c}
	if e = s.bootstrap(ctx); e != nil {
		s.Close()
		return nil, dbError(e)
	}
	return s, nil
}
func (s *Service) operation(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, raw.NewError(raw.CodeUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(s.stop, cancel)
	s.wg.Add(1)
	return ctx, func() { stop(); cancel(); s.wg.Done() }, nil
}
func (s *Service) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		s.pool.Close()
	})
}

// databaseError retains only allowlisted public information, never the cause.
// In particular, wrapping a raw.Error must not retain an unsafe outer message.
type databaseError struct {
	public   *raw.Error
	canceled bool
	deadline bool
}

func (e *databaseError) Error() string { return e.public.Error() }
func (e *databaseError) Unwrap() error { return e.public }
func (e *databaseError) Is(target error) bool {
	return (target == context.Canceled && e.canceled) ||
		(target == context.DeadlineExceeded && e.deadline)
}
func (e *databaseError) MarshalJSON() ([]byte, error) { return json.Marshal(e.public) }

func dbError(e error) error {
	if e == nil {
		return nil
	}
	safe := &databaseError{
		canceled: errors.Is(e, context.Canceled),
		deadline: errors.Is(e, context.DeadlineExceeded),
	}
	code := raw.CodeUnavailable
	var r *raw.Error
	var p *pgconn.PgError
	switch {
	case errors.As(e, &r) && r != nil:
		code = r.Code
	case safe.canceled || safe.deadline:
		code = raw.CodeTimeout
	case errors.As(e, &p) && p != nil && (p.Code == "57014" || p.Code == "55P03"):
		code = raw.CodeTimeout
	}
	safe.public = raw.NewError(code)
	return safe
}
func (s *Service) transaction(ctx context.Context, isolation pgx.TxIsoLevel, fn func(context.Context, pgx.Tx) error) error {
	ctx, done, e := s.operation(ctx)
	if e != nil {
		return e
	}
	defer done()
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
	if e != nil {
		return dbError(e)
	}
	defer tx.Rollback(context.Background())
	if e = fn(ctx, tx); e != nil {
		return dbError(e)
	}
	return dbError(tx.Commit(ctx))
}

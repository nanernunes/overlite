package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"overlite/core"
)

/*
Advisory locks: pg_advisory_lock and its family, which migration tools take so
two deploys never migrate at once (Prisma, Rails, Flyway, golang-migrate).
SQLite has nothing like them, so they live here, in the server: one registry
for every connection, a lock per database and key, owned by sessions.

Like nextval, a call is evaluated before the statement reaches SQLite and
replaced by its result, so `SELECT pg_try_advisory_lock(42)` runs as
`SELECT 1 AS pg_try_advisory_lock`. A session-level lock lasts until it is
unlocked as many times as it was taken, or the connection closes; a
transaction-level one (the _xact_ forms) until the transaction ends. A lock
call that has to wait does, until the holder lets go or the client cancels.
*/

// advisoryKey names a lock. The one-bigint and two-int4 forms are separate
// key spaces, as in Postgres.
type advisoryKey struct {
	database string
	pair     bool
	a, b     int64
}

// advisoryHold is who holds a lock: at most one session exclusively, any
// number shared, each as many times as it took it.
type advisoryHold struct {
	exclusive *session
	exclCount int
	shared    map[*session]int
}

var advisoryLocks = struct {
	mu    sync.Mutex
	holds map[advisoryKey]*advisoryHold
	// released is closed, and replaced, whenever a lock is let go, waking
	// every waiter to try again.
	released chan struct{}
}{holds: map[advisoryKey]*advisoryHold{}, released: make(chan struct{})}

// advisoryFuncs are the functions handled here, by name.
var advisoryFuncs = map[string]bool{
	"pg_advisory_lock": true, "pg_advisory_lock_shared": true,
	"pg_try_advisory_lock": true, "pg_try_advisory_lock_shared": true,
	"pg_advisory_xact_lock": true, "pg_advisory_xact_lock_shared": true,
	"pg_try_advisory_xact_lock": true, "pg_try_advisory_xact_lock_shared": true,
	"pg_advisory_unlock": true, "pg_advisory_unlock_shared": true,
	"pg_advisory_unlock_all": true,
}

func hasAdvisoryFunc(sql string) bool {
	return strings.Contains(strings.ToLower(sql), "advisory_")
}

// expandAdvisoryLocks evaluates every advisory lock call in sql and replaces it
// with its result: ” for the void ones, 1/0 for the rest. A statement that is
// just the call keeps the column name Postgres gives it.
func (s *session) expandAdvisoryLocks(sql string, params []core.Value) (string, error) {
	if !hasAdvisoryFunc(sql) {
		return sql, nil
	}
	calls := 0
	out, err := scanFuncCalls(sql, isAdvisoryFunc, func(fn, args string) (string, error) {
		calls++
		return s.evalAdvisoryFunc(fn, args, params)
	})
	if err != nil {
		return "", err
	}
	return nameSoleCall(sql, out, calls), nil
}

// neutralizeAdvisoryLocks stands every call in for a result of its type,
// taking no lock, for Describe.
func neutralizeAdvisoryLocks(sql string) string {
	if !hasAdvisoryFunc(sql) {
		return sql
	}
	calls := 0
	out, _ := scanFuncCalls(sql, isAdvisoryFunc, func(fn, _ string) (string, error) {
		calls++
		return advisoryResult(fn, true), nil
	})
	return nameSoleCall(sql, out, calls)
}

func isAdvisoryFunc(fn string) bool { return advisoryFuncs[fn] }

// nameSoleCall aliases the result of `SELECT pg_…(…)` after the function, as
// Postgres names that column, so a client reading it by name finds it.
func nameSoleCall(orig, expanded string, calls int) string {
	if calls != 1 {
		return expanded
	}
	f := strings.Fields(strings.TrimRight(strings.TrimSpace(orig), ";"))
	if len(f) < 2 || !strings.EqualFold(f[0], "SELECT") {
		return expanded
	}
	rest := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(orig), ";"))[len(f[0]):]
	rest = strings.TrimSpace(rest)
	open := strings.IndexByte(rest, '(')
	if open < 0 {
		return expanded
	}
	fn := strings.ToLower(strings.TrimSpace(rest[:open]))
	if !advisoryFuncs[fn] {
		return expanded
	}
	if _, end, ok := readParenArgs(rest, open); !ok || strings.TrimSpace(rest[end:]) != "" {
		return expanded
	}
	return strings.TrimRight(strings.TrimSpace(expanded), ";") + " AS " + fn
}

// advisoryResult is what a call returns, as a literal.
func advisoryResult(fn string, ok bool) string {
	switch fn {
	case "pg_advisory_lock", "pg_advisory_lock_shared",
		"pg_advisory_xact_lock", "pg_advisory_xact_lock_shared":
		return "''" // void
	}
	if ok {
		return "1"
	}
	return "0"
}

func (s *session) evalAdvisoryFunc(fn, args string, params []core.Value) (string, error) {
	if fn == "pg_advisory_unlock_all" {
		releaseAdvisoryLocks(s)
		return "''", nil
	}
	key, err := s.advisoryKey(args, params)
	if err != nil {
		return "", fmt.Errorf("%s: %w", fn, err)
	}
	shared := strings.HasSuffix(fn, "_shared")
	switch {
	case strings.HasPrefix(fn, "pg_advisory_unlock"):
		return advisoryResult(fn, unlockAdvisory(s, key, shared)), nil
	case strings.HasPrefix(fn, "pg_try_"):
		ok := tryAdvisory(s, key, shared)
		if ok && strings.Contains(fn, "_xact_") {
			s.holdForTx(key, shared)
		}
		return advisoryResult(fn, ok), nil
	default:
		ctx, cancel := s.armCancel()
		defer cancel()
		if err := waitAdvisory(ctx, s, key, shared); err != nil {
			return "", err
		}
		if strings.Contains(fn, "_xact_") {
			s.holdForTx(key, shared)
		}
		return advisoryResult(fn, true), nil
	}
}

// advisoryKey reads a call's one bigint or two int4 arguments: literals, casts
// of them, or parameters.
func (s *session) advisoryKey(args string, params []core.Value) (advisoryKey, error) {
	parts := splitTop(args)
	if len(parts) != 1 && len(parts) != 2 {
		return advisoryKey{}, errors.New("expects one bigint or two integer keys")
	}
	nums := make([]int64, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if j := strings.Index(p, "::"); j >= 0 {
			p = strings.TrimSpace(p[:j])
		}
		p = strings.TrimSpace(strings.Trim(p, "()"))
		if strings.HasPrefix(p, "$") {
			n, ok := parseInt(p[1:])
			if !ok || n < 1 || int(n) > len(params) {
				return advisoryKey{}, fmt.Errorf("no value for parameter %s", p)
			}
			p = strings.TrimSpace(asString(params[n-1]))
		}
		v, ok := parseInt(strings.Trim(p, "'"))
		if !ok {
			return advisoryKey{}, fmt.Errorf("key %q is not an integer", parts[i])
		}
		nums[i] = v
	}
	key := advisoryKey{database: s.database, a: nums[0]}
	if len(nums) == 2 {
		key.pair, key.b = true, nums[1]
	}
	return key, nil
}

// canTake reports whether s may take the lock now: exclusively when nobody
// else holds it at all, shared when nobody else holds it exclusively.
func (h *advisoryHold) canTake(s *session, shared bool) bool {
	if h == nil {
		return true
	}
	if h.exclusive != nil && h.exclusive != s {
		return false
	}
	if shared {
		return true
	}
	for holder := range h.shared {
		if holder != s {
			return false
		}
	}
	return true
}

func (h *advisoryHold) take(s *session, shared bool) {
	if shared {
		h.shared[s]++
		return
	}
	h.exclusive = s
	h.exclCount++
}

// tryAdvisory takes the lock if it is free to s, without waiting.
func tryAdvisory(s *session, key advisoryKey, shared bool) bool {
	advisoryLocks.mu.Lock()
	defer advisoryLocks.mu.Unlock()
	h := advisoryLocks.holds[key]
	if !h.canTake(s, shared) {
		return false
	}
	if h == nil {
		h = &advisoryHold{shared: map[*session]int{}}
		advisoryLocks.holds[key] = h
	}
	h.take(s, shared)
	return true
}

// waitAdvisory takes the lock, waiting for it as long as ctx allows.
func waitAdvisory(ctx context.Context, s *session, key advisoryKey, shared bool) error {
	for {
		advisoryLocks.mu.Lock()
		released := advisoryLocks.released
		advisoryLocks.mu.Unlock()
		if tryAdvisory(s, key, shared) {
			return nil
		}
		select {
		case <-released:
		case <-ctx.Done():
			return errors.New("canceling statement due to user request")
		}
	}
}

// unlockAdvisory lets go of one hold s has on the lock, reporting whether it
// had one.
func unlockAdvisory(s *session, key advisoryKey, shared bool) bool {
	advisoryLocks.mu.Lock()
	defer advisoryLocks.mu.Unlock()
	h := advisoryLocks.holds[key]
	if h == nil {
		return false
	}
	switch {
	case shared && h.shared[s] > 0:
		if h.shared[s]--; h.shared[s] == 0 {
			delete(h.shared, s)
		}
	case !shared && h.exclusive == s:
		if h.exclCount--; h.exclCount == 0 {
			h.exclusive = nil
		}
	default:
		return false
	}
	if h.exclusive == nil && len(h.shared) == 0 {
		delete(advisoryLocks.holds, key)
	}
	wakeAdvisoryWaiters()
	return true
}

// releaseAdvisoryLocks lets go of every lock s holds: pg_advisory_unlock_all,
// and the connection closing.
func releaseAdvisoryLocks(s *session) {
	advisoryLocks.mu.Lock()
	defer advisoryLocks.mu.Unlock()
	for key, h := range advisoryLocks.holds {
		if h.exclusive == s {
			h.exclusive, h.exclCount = nil, 0
		}
		delete(h.shared, s)
		if h.exclusive == nil && len(h.shared) == 0 {
			delete(advisoryLocks.holds, key)
		}
	}
	s.txAdvisory = nil
	wakeAdvisoryWaiters()
}

// wakeAdvisoryWaiters tells every waiting lock call to try again. The caller
// holds advisoryLocks.mu.
func wakeAdvisoryWaiters() {
	close(advisoryLocks.released)
	advisoryLocks.released = make(chan struct{})
}

// txAdvisoryHold is a lock taken for the current transaction.
type txAdvisoryHold struct {
	key    advisoryKey
	shared bool
}

// holdForTx records a transaction-level lock, to let go when the transaction
// ends; outside one, the statement is the transaction, so it goes at once.
func (s *session) holdForTx(key advisoryKey, shared bool) {
	if s.tx == nil {
		unlockAdvisory(s, key, shared)
		return
	}
	s.txAdvisory = append(s.txAdvisory, txAdvisoryHold{key, shared})
}

// releaseTxAdvisoryLocks lets go of the transaction's locks as it ends.
func (s *session) releaseTxAdvisoryLocks() {
	for _, h := range s.txAdvisory {
		unlockAdvisory(s, h.key, h.shared)
	}
	s.txAdvisory = nil
}

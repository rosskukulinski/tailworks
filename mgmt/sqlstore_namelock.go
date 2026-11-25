package mgmt

import (
	"context"
	"hash/fnv"
	"sync"
)

// Named locks across replicas (#578).
//
// Some writes are a read-modify-write of one custody entry (a workload's
// secrets: secrets.go). A mutex serialises them within one process, but
// replicas sharing a Postgres store and a setec custody each have their
// own, and an acknowledged write could be lost. LockName serialises them
// across every replica of the store.

// nameLocker is implemented by stores that can lock a name across every
// process that shares them.
type nameLocker interface {
	// LockName blocks until it holds key (or ctx ends) and returns the
	// function that releases it.
	LockName(ctx context.Context, key string) (unlock func(), err error)
}

// sqliteNameLocks: on SQLite one process owns the store, so a mutex per
// key is enough.
var sqliteNameLocks sync.Map // key → *sync.Mutex

// LockName holds a session-level Postgres advisory lock on a connection of
// its own (so two callers in one process exclude each other too), until
// unlock; if the replica dies, Postgres ends the session and frees it. On
// SQLite it is a process mutex.
func (s *SQLStore) LockName(ctx context.Context, key string) (func(), error) {
	if s.d != dialectPostgres {
		v, _ := sqliteNameLocks.LoadOrStore(key, &sync.Mutex{})
		mu := v.(*sync.Mutex)
		mu.Lock()
		return mu.Unlock, nil
	}
	h := fnv.New64a()
	h.Write([]byte("tw-name-lock|" + key))
	id := int64(h.Sum64())
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", id); err != nil {
		conn.Close()
		return nil, err
	}
	return func() {
		conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", id)
		conn.Close()
	}, nil
}

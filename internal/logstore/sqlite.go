//go:build !nosqlite

package logstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go, no cgo: release binaries stay static
)

// migrations[i] upgrades schema version i to i+1 (PRAGMA user_version).
var migrations = []string{
	`CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL,
		ts_ns INTEGER NOT NULL,
		level TEXT NOT NULL,
		event TEXT NOT NULL,
		ecosystem TEXT NOT NULL DEFAULT '',
		package TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		repo TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL DEFAULT '',
		commit_sha TEXT NOT NULL DEFAULT '',
		credential_source TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		duration_ms INTEGER NOT NULL DEFAULT 0,
		http_status INTEGER NOT NULL DEFAULT 0,
		error_code TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT '',
		fields TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX events_ts ON events(ts_ns);
	CREATE INDEX events_package ON events(package);
	CREATE INDEX events_event ON events(event);`,
}

// SchemaVersion is the current schema version.
var SchemaVersion = len(migrations)

type sqliteStore struct {
	db   *sql.DB
	path string
}

func openSQLite(path string) (Store, error) {
	// Create the file with 0600 before SQLite does (it would use 0644).
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		f.Close()
	} else {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	q := url.Values{}
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)", "auto_vacuum(INCREMENTAL)", "foreign_keys(ON)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &sqliteStore{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		_ = os.Chmod(path+sfx, 0o600)
	}
	return s, nil
}

func (s *sqliteStore) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("log database schema v%d is newer than this consona (v%d)", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var cur int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&cur); err != nil {
			tx.Rollback()
			return err
		}
		if cur > v { // another process migrated meanwhile
			tx.Rollback()
			v = cur - 1
			continue
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrating log database to v%d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// A cheap read proves the database is usable (corrupt files fail here).
	var n int
	return s.db.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT 1 FROM events LIMIT 1)").Scan(&n)
}

const cols = "id, ts, level, event, ecosystem, package, owner, repo, version, commit_sha, credential_source, result, duration_ms, http_status, error_code, message, fields"

func (s *sqliteStore) Append(entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO events (ts, ts_ns, level, event, ecosystem, package, owner, repo, version, commit_sha,
		credential_source, result, duration_ms, http_status, error_code, message, fields) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer st.Close()
	for _, e := range entries {
		e = normalize(e)
		var fields string
		if e.Fields != nil {
			b, _ := json.Marshal(e.Fields)
			fields = string(b)
		}
		if _, err := st.Exec(e.TS.Format(time.RFC3339Nano), e.TS.UnixNano(), e.Level, e.Event, e.Ecosystem, e.Package, e.Owner,
			e.Repo, e.Version, e.CommitSHA, e.CredentialSource, e.Result, e.DurationMS, e.HTTPStatus, e.ErrorCode, e.Message, fields); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *sqliteStore) Query(f Filter) ([]Entry, error) {
	var where []string
	var args []any
	if f.Package != "" {
		where, args = append(where, "package = ? COLLATE NOCASE"), append(args, f.Package)
	}
	if f.Owner != "" {
		where, args = append(where, "owner = ? COLLATE NOCASE"), append(args, strings.TrimLeft(f.Owner, "@"))
	}
	if f.Event != "" {
		p := strings.TrimSuffix(f.Event, ".")
		where, args = append(where, "(event = ? OR substr(event, 1, ?) = ?)"), append(args, f.Event, len(p)+1, p+".")
	}
	if f.Level != "" {
		var ls []string
		for l, n := range levels {
			if n >= levels[f.Level] {
				ls = append(ls, "'"+l+"'")
			}
		}
		where = append(where, "level IN ("+strings.Join(ls, ",")+")")
	}
	if !f.Since.IsZero() {
		where, args = append(where, "ts_ns >= ?"), append(args, f.Since.UnixNano())
	}
	if !f.Until.IsZero() {
		where, args = append(where, "ts_ns <= ?"), append(args, f.Until.UnixNano())
	}
	if f.ErrorsOnly {
		where = append(where, "(level = 'error' OR result = 'fail')")
	}
	q := "SELECT " + cols + " FROM events"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ts_ns DESC, id DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts, fields string
		if err := rows.Scan(&e.ID, &ts, &e.Level, &e.Event, &e.Ecosystem, &e.Package, &e.Owner, &e.Repo, &e.Version, &e.CommitSHA,
			&e.CredentialSource, &e.Result, &e.DurationMS, &e.HTTPStatus, &e.ErrorCode, &e.Message, &fields); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(time.RFC3339Nano, ts)
		if fields != "" {
			_ = json.Unmarshal([]byte(fields), &e.Fields)
		}
		out = append(out, e)
	}
	return limit(out, 0), rows.Err()
}

func (s *sqliteStore) size() int64 {
	var pc, ps int64
	_ = s.db.QueryRow("PRAGMA page_count").Scan(&pc)
	_ = s.db.QueryRow("PRAGMA page_size").Scan(&ps)
	return pc * ps
}

func (s *sqliteStore) Prune(r Retention) (int64, error) {
	var removed int64
	if r.MaxAge > 0 {
		res, err := s.db.Exec("DELETE FROM events WHERE ts_ns < ?", time.Now().Add(-r.MaxAge).UnixNano())
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	if r.MaxSize > 0 {
		for i := 0; i < 20 && s.size() > r.MaxSize; i++ {
			var count int64
			if err := s.db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count == 0 {
				break
			}
			// Drop the oldest quarter (at least one row), then reclaim pages.
			res, err := s.db.Exec("DELETE FROM events WHERE id IN (SELECT id FROM events ORDER BY ts_ns, id LIMIT ?)", max(count/4, 1))
			if err != nil {
				return removed, err
			}
			n, _ := res.RowsAffected()
			removed += n
			if _, err := s.db.Exec("PRAGMA incremental_vacuum"); err != nil {
				return removed, err
			}
		}
	}
	if removed > 0 {
		_, _ = s.db.Exec("PRAGMA incremental_vacuum")
		_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	}
	return removed, nil
}

func (s *sqliteStore) Clear() error {
	if _, err := s.db.Exec("DELETE FROM events"); err != nil {
		return err
	}
	_, _ = s.db.Exec("PRAGMA incremental_vacuum")
	_, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func (s *sqliteStore) Info() (Info, error) {
	in := Info{Backend: "sqlite", Path: s.path}
	var oldest, newest sql.NullInt64
	if err := s.db.QueryRow("SELECT count(*), min(ts_ns), max(ts_ns) FROM events").Scan(&in.Count, &oldest, &newest); err != nil {
		return in, err
	}
	if oldest.Valid {
		in.Oldest, in.Newest = time.Unix(0, oldest.Int64).UTC(), time.Unix(0, newest.Int64).UTC()
	}
	for _, sfx := range []string{"", "-wal"} {
		if st, err := os.Stat(s.path + sfx); err == nil {
			in.Size += st.Size()
		}
	}
	return in, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

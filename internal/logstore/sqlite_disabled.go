//go:build nosqlite

package logstore

func openSQLite(string) (Store, error) { return nil, ErrNoSQLite }

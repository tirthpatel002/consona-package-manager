package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

var logsUsage = `consona logs: structured event log (SQLite, or JSON lines as a fallback)

  consona logs [--package @o/n] [--owner o] [--event build] [--level warn] [--errors]
               [--since 2h|2026-10-26] [--until ...] [--limit 50] [--json]
  consona logs export [--format jsonl|json] [-o file] [filters]
  consona logs prune                 apply retention now (CONSONA_LOG_MAX_AGE, default 30d;
                                     CONSONA_LOG_MAX_SIZE_MB, default 50)
  consona logs clear [--yes]         delete every entry (asks first)
  consona logs info                  backend in use, path, counts

Events: ` + strings.Join(logstore.Events, ", ") + `
Backend: CONSONA_LOG_BACKEND=sqlite|jsonl (default: SQLite, JSON lines if SQLite is unavailable).
Token values are never stored; every field is redacted before it is written.
`

func runLogs(cfg daemon.Config, args []string) int {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "help" {
		fmt.Print(logsUsage)
		return 0
	}
	s, err := logstore.Open(config.LogDir())
	if err != nil {
		return fail("%v", err)
	}
	defer s.Close()
	f, format, out, asJSON, yes, err := parseLogFlags(args)
	if err != nil {
		return fail("%v\n%s", err, logsUsage)
	}
	switch sub {
	case "":
		if f.Limit == 0 {
			f.Limit = 100
		}
		es, err := s.Query(f)
		if err != nil {
			return fail("%v", err)
		}
		if asJSON {
			enc := json.NewEncoder(os.Stdout)
			for _, e := range es {
				_ = enc.Encode(e)
			}
			return 0
		}
		for _, e := range es {
			fmt.Printf("%s %s\n", e.TS.Local().Format("2006-01-02 15:04:05.000"), logstore.Human(e))
		}
		if len(es) == 0 {
			fmt.Fprintln(os.Stderr, "consona: no matching log entries")
		}
		return 0
	case "export":
		var w io.Writer = os.Stdout
		if out != "" && out != "-" {
			fh, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return fail("%v", err)
			}
			defer fh.Close()
			bw := bufio.NewWriter(fh)
			defer bw.Flush()
			w = bw
		}
		n, err := logstore.Export(s, w, format, f)
		if err != nil {
			return fail("%v", err)
		}
		if out != "" && out != "-" {
			fmt.Fprintf(os.Stderr, "consona: exported %d entries to %s\n", n, out)
		}
		return 0
	case "prune":
		r := logstore.DefaultRetention()
		n, err := s.Prune(r)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("consona: pruned %d entries (max age %s, max size %d MB)\n", n, r.MaxAge, r.MaxSize>>20)
		return 0
	case "clear":
		if !yes {
			fmt.Fprint(os.Stderr, "Delete every consona log entry? [y/N] ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
				fmt.Fprintln(os.Stderr, "consona: not cleared")
				return 1
			}
		}
		if err := s.Clear(); err != nil {
			return fail("%v", err)
		}
		fmt.Println("consona: log cleared")
		return 0
	case "info":
		in, err := s.Info()
		if err != nil {
			return fail("%v", err)
		}
		kv := [][2]string{{"backend", in.Backend}, {"path", in.Path}, {"entries", fmt.Sprint(in.Count)},
			{"size", fmt.Sprintf("%.1f KB", float64(in.Size)/1024)}}
		if !in.Oldest.IsZero() {
			kv = append(kv, [2]string{"oldest", in.Oldest.Local().Format(time.RFC3339)}, [2]string{"newest", in.Newest.Local().Format(time.RFC3339)})
		}
		r := logstore.DefaultRetention()
		kv = append(kv, [2]string{"retention", fmt.Sprintf("%s, %d MB", r.MaxAge, r.MaxSize>>20)})
		if in.Fallback != "" {
			kv = append(kv, [2]string{"fallback", in.Fallback})
		}
		kv = append(kv, [2]string{"human log", cfg.LogPath()}, [2]string{"config", config.Dir()})
		config.PrintKV(os.Stdout, kv...)
		return 0
	}
	fmt.Fprint(os.Stderr, logsUsage)
	return 2
}

func parseTime(v string, now time.Time) (time.Time, error) {
	if d, err := logstore.ParseAge(v); err == nil {
		return now.Add(-d), nil
	}
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad time %q (use e.g. 2h, 7d, 2026-10-26 or RFC 3339)", v)
}

func parseLogFlags(args []string) (f logstore.Filter, format, out string, asJSON, yes bool, err error) {
	now := time.Now()
	for i := 0; i < len(args); i++ {
		a := args[i]
		k, v, hasV := strings.Cut(a, "=")
		need := func() (string, error) {
			if hasV {
				return v, nil
			}
			if i+1 < len(args) {
				i++
				return args[i], nil
			}
			return "", fmt.Errorf("%s needs a value", k)
		}
		var val string
		switch k {
		case "--json":
			asJSON = true
			continue
		case "--errors":
			f.ErrorsOnly = true
			continue
		case "--yes", "-y":
			yes = true
			continue
		case "--package", "--owner", "--event", "--level", "--since", "--until", "--limit", "--format", "-o", "--output":
			if val, err = need(); err != nil {
				return
			}
		default:
			err = fmt.Errorf("unknown option %q", a)
			return
		}
		switch k {
		case "--package":
			f.Package = val
		case "--owner":
			f.Owner = val
		case "--event":
			f.Event = val
		case "--level":
			f.Level = val
		case "--since":
			f.Since, err = parseTime(val, now)
		case "--until":
			f.Until, err = parseTime(val, now)
		case "--limit":
			f.Limit, err = strconv.Atoi(val)
		case "--format":
			format = val
		case "-o", "--output":
			out = val
		}
		if err != nil {
			return
		}
	}
	return
}

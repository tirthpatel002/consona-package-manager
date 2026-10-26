package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/tirthpatel002/consona-package-manager/internal/creds"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/redact"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"
)

const tokenUsage = `consona token: optional named GitHub tokens (values in the OS secure store)

  consona token add <name> [--for @owner ...] [--description "..."] [--default] [--replace] [--no-verify]
        reads the token from a hidden prompt, or from stdin when piped:
          consona token add work --for @acme            (prompts)
          printf '%s' "$TOKEN" | consona token add ci --default
        never pass a token as an argument (shell history, process list).
  consona token list                 names, owners, login, type, expiry (never values)
  consona token test [name]          check a token (or every available credential) against GitHub
  consona token remove <name>        delete the token from the secure store and the index

Values live only in the OS secure store (service "dev.consona.tokens"):
macOS Keychain, Windows Credential Manager, Linux Secret Service. The index
(metadata only) is ` + "`<config dir>/consona/tokens.json`" + ` (0600). Without a
secure store (headless Linux), use env vars or gh instead; consona never
writes tokens to a plaintext file.

` + creds.Order + `
`

type tokenCmd struct {
	store     creds.Store
	indexPath string
	src       *github.Source
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	isTTY     func() bool
	readHide  func() ([]byte, error)
	now       func() time.Time
}

func newTokenCmd() *tokenCmd {
	src := github.New()
	return &tokenCmd{
		store: creds.SystemStore{}, indexPath: creds.IndexPath(), src: src,
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		isTTY:    func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		readHide: func() ([]byte, error) { return term.ReadPassword(int(os.Stdin.Fd())) },
		now:      time.Now,
	}
}

func (t *tokenCmd) failf(format string, a ...any) int {
	fmt.Fprintf(t.stderr, "consona: "+format+"\n", a...)
	return 1
}

func (t *tokenCmd) run(ctx context.Context, args []string) int {
	// A token on the command line ends up in shell history and the process
	// list: refuse it outright, whatever the subcommand.
	for _, a := range args {
		if a == "--token" || strings.HasPrefix(a, "--token=") || strings.HasPrefix(a, "--value") ||
			creds.TokenType(a) != "unknown" {
			return t.failf("refusing a token on the command line (it would be kept in your shell history and visible in the process list).\n" +
				"  Revoke it if it was a real token, then pipe it or type it at the hidden prompt: consona token add <name> [--for @owner]")
		}
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		io.WriteString(t.stdout, tokenUsage)
		return 0
	}
	switch args[0] {
	case "add":
		return t.add(ctx, args[1:])
	case "list", "ls":
		return t.list()
	case "test", "check":
		return t.test(ctx, args[1:])
	case "remove", "rm", "delete":
		return t.remove(args[1:])
	}
	io.WriteString(t.stderr, tokenUsage)
	return 2
}

func (t *tokenCmd) readSecret(name string) (string, error) {
	var raw []byte
	var err error
	if t.isTTY() {
		fmt.Fprintf(t.stderr, "GitHub token for %q (input hidden): ", name)
		raw, err = t.readHide()
		fmt.Fprintln(t.stderr)
	} else {
		raw, err = io.ReadAll(io.LimitReader(bufio.NewReader(t.stdin), 64<<10))
	}
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("no token given (type it at the prompt or pipe it on stdin)")
	}
	if strings.ContainsAny(tok, " \t\r\n") {
		return "", errors.New("the token must be a single line without spaces")
	}
	redact.AddSecret(tok)
	return tok, nil
}

func (t *tokenCmd) add(ctx context.Context, args []string) int {
	var name, desc string
	var owners []string
	var def, replace, noVerify bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() (string, bool) {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
				return v, true
			}
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch {
		case a == "--for" || strings.HasPrefix(a, "--for="):
			v, ok := val()
			if !ok {
				return t.failf("--for needs an owner, e.g. --for @acme")
			}
			for _, o := range strings.Split(v, ",") {
				o = creds.NormOwner(o)
				if err := creds.ValidOwner(o); err != nil {
					return t.failf("%v", err)
				}
				owners = append(owners, o)
			}
		case a == "--description" || strings.HasPrefix(a, "--description="):
			v, ok := val()
			if !ok {
				return t.failf("--description needs a value")
			}
			desc = v
		case a == "--default":
			def = true
		case a == "--replace":
			replace = true
		case a == "--no-verify":
			noVerify = true
		case strings.HasPrefix(a, "-"):
			return t.failf("unknown option %q (see `consona token help`)", a)
		case name == "":
			name = a
		default:
			return t.failf("unexpected argument %q: the token itself is read from the prompt or stdin, never from arguments", a)
		}
	}
	if name == "" {
		return t.failf("usage: consona token add <name> [--for @owner ...] [--description \"...\"] [--default] [--replace]")
	}
	if err := creds.ValidName(name); err != nil {
		return t.failf("%v", err)
	}
	ix, err := creds.LoadIndex(t.indexPath)
	if err != nil {
		return t.failf("%v", err)
	}
	if err := ix.Add(creds.Token{Name: name}, replace); err != nil { // uniqueness check before prompting
		return t.failf("%v", err)
	}
	ix, _ = creds.LoadIndex(t.indexPath)
	tok, err := t.readSecret(name)
	if err != nil {
		return t.failf("%v", err)
	}
	if old := ix.Find(name); old != nil && replace {
		name = old.Name // the secure store key is case-sensitive
		if owners == nil {
			owners = old.Owners // --replace without --for keeps the mapping
		}
		if desc == "" {
			desc = old.Description
		}
		def = def || old.Default
	}
	entry := creds.Token{Name: name, Owners: owners, Description: desc, Default: def, Type: creds.TokenType(tok), Added: t.now().UTC()}
	cand := creds.Candidate{Source: fmt.Sprintf("named token %q", name), Kind: "named", Name: name, Value: tok}
	var meta *github.Meta
	if !noVerify {
		m, fail, err := t.src.Inspect(ctx, cand)
		if err != nil {
			return t.failf("checking the token with GitHub: %v (use --no-verify to store it without checking)", redact.String(err.Error()))
		}
		if fail != nil {
			if fail.Kind == github.KindUnauthorized || fail.Kind == github.KindExpired {
				return t.failf("GitHub rejected this token, nothing was stored: %s.\n  Fix: %s", fail.Detail, fail.Fix)
			}
			fmt.Fprintf(t.stderr, "consona: warning: GET /user failed: %s.\n  Fix: %s\n", fail.Detail, fail.Fix)
		}
		if m != nil {
			meta = m
			entry.Login, entry.UserName, entry.UserID, entry.Scopes = m.Login, m.Name, m.ID, m.Scopes
			if !m.Expires.IsZero() {
				entry.Expires = m.Expires.UTC().Format(time.RFC3339)
			}
			entry.LastChecked = t.now().UTC()
		}
	}
	if err := t.store.Set(name, tok); err != nil {
		return t.failf("cannot store token %q: %v", name, err)
	}
	if err := ix.Add(entry, replace); err != nil {
		return t.failf("%v", err)
	}
	if err := ix.Save(t.indexPath); err != nil {
		_ = t.store.Delete(name)
		return t.failf("saving the token index: %v (the token was removed from the secure store again)", err)
	}
	verb := "added"
	if replace {
		verb = "saved"
	}
	fmt.Fprintf(t.stdout, "consona: %s token %q (%s) to the OS secure store; index: %s\n", verb, name, redact.Mask(tok), t.indexPath)
	t.printMeta(entry, meta)
	if meta != nil {
		t.printProbes(ctx, cand, entry)
	}
	logstore.Log(logstore.Entry{Event: logstore.EvTokenAdd, Level: "info", CredentialSource: cand.Source, Result: "ok",
		Owner: strings.Join(owners, ","), Message: fmt.Sprintf("type %s, login %s", entry.Type, orDash(entry.Login))})
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (t *tokenCmd) printMeta(e creds.Token, m *github.Meta) {
	w := t.stdout
	typ := creds.TypeDescription(e.Type)
	if typ == "" {
		typ = "unknown token format"
	}
	fmt.Fprintf(w, "  type:        %s\n", typ)
	if m == nil {
		fmt.Fprintf(w, "  GitHub:      not checked\n")
	} else {
		fmt.Fprintf(w, "  account:     %s", m.Login)
		if m.Name != "" {
			fmt.Fprintf(w, " (%s)", m.Name)
		}
		fmt.Fprintf(w, ", id %d\n", m.ID)
		switch {
		case !m.Expires.IsZero():
			left := m.Expires.Sub(t.now())
			fmt.Fprintf(w, "  expires:     %s (in %d days)\n", m.Expires.Local().Format("2006-01-02 15:04 MST"), int(left.Hours()/24))
		default:
			fmt.Fprintf(w, "  expires:     never (no expiry reported by GitHub)\n")
		}
		if m.ScopesKnown {
			sc := strings.Join(m.Scopes, ", ")
			if sc == "" {
				sc = "none"
			}
			fmt.Fprintf(w, "  scopes:      %s\n", sc)
			if e.Type == "classic" && !strings.Contains(","+strings.ReplaceAll(sc, " ", "")+",", ",repo,") {
				fmt.Fprintf(w, "  warning:     classic token without the `repo` scope cannot read private repos; add it at %s\n", github.ClassicURL)
			}
		}
		if m.RateLimit > 0 {
			fmt.Fprintf(w, "  rate limit:  %d/%d remaining", m.RateRemaining, m.RateLimit)
			if !m.RateReset.IsZero() {
				fmt.Fprintf(w, " (resets %s)", m.RateReset.Local().Format("15:04 MST"))
			}
			fmt.Fprintln(w)
		}
	}
	if len(e.Owners) > 0 {
		fmt.Fprintf(w, "  for:         @%s\n", strings.Join(e.Owners, ", @"))
	}
	if e.Default {
		fmt.Fprintf(w, "  default:     yes (used for owners without a more specific credential)\n")
	}
	if e.Description != "" {
		fmt.Fprintf(w, "  description: %s\n", e.Description)
	}
	if e.Type == "fine-grained" {
		fmt.Fprintf(w, "  note:        GitHub's API does not expose a fine-grained token's name, description or permission list;\n"+
			"               consona checks what the token can actually do instead:\n")
	}
}

func (t *tokenCmd) printProbes(ctx context.Context, c creds.Candidate, e creds.Token) int {
	owners := e.Owners
	if len(owners) == 0 && e.Login != "" {
		owners = []string{e.Login}
	}
	fails := 0
	for _, p := range t.src.Probes(ctx, c, owners) {
		if p.OK {
			fmt.Fprintf(t.stdout, "  PASS  %s: %s\n", p.Name, p.Detail)
		} else {
			fails++
			fmt.Fprintf(t.stdout, "  FAIL  %s: %s\n        fix: %s\n", p.Name, p.Detail, p.Fix)
		}
	}
	return fails
}

func (t *tokenCmd) list() int {
	ix, err := creds.LoadIndex(t.indexPath)
	if err != nil {
		return t.failf("%v", err)
	}
	if len(ix.Tokens) == 0 {
		fmt.Fprintf(t.stdout, "no named tokens (add one with `consona token add <name> --for @owner`)\n")
	} else {
		tw := tabwriter.NewWriter(t.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tFOR\tLOGIN\tTYPE\tEXPIRES\tADDED\tDESCRIPTION")
		for _, e := range ix.Tokens {
			name := e.Name
			if e.Default {
				name += " (default)"
			}
			owners := "-"
			if len(e.Owners) > 0 {
				owners = "@" + strings.Join(e.Owners, ",@")
			}
			exp := "never"
			if x := e.ExpiresAt(); !x.IsZero() {
				exp = x.Local().Format("2006-01-02")
				if x.Before(t.now()) {
					exp += " EXPIRED"
				}
			} else if e.LastChecked.IsZero() {
				exp = "unknown"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, owners, orDash(e.Login), orDash(e.Type), exp,
				e.Added.Local().Format("2006-01-02"), orDash(e.Description))
		}
		tw.Flush()
	}
	kv := [][2]string{{"index", t.indexPath}, {"secrets", "OS secure store, service " + creds.Service}}
	if vars := tokenEnvVars(); len(vars) > 0 {
		kv = append(kv, [2]string{"env", strings.Join(vars, ", ") + " (also used, by name only)"})
	}
	fmt.Fprintln(t.stdout)
	config.PrintKV(t.stdout, kv...)
	return 0
}

func (t *tokenCmd) test(ctx context.Context, args []string) int {
	ix, err := creds.LoadIndex(t.indexPath)
	if err != nil {
		return t.failf("%v", err)
	}
	type item struct {
		c creds.Candidate
		e creds.Token
	}
	var items []item
	if len(args) > 0 {
		e := ix.Find(args[0])
		if e == nil {
			return t.failf("no named token %q (see `consona token list`)", args[0])
		}
		v, err := t.store.Get(e.Name)
		if err != nil {
			return t.failf("token %q: %v", e.Name, err)
		}
		redact.AddSecret(v)
		items = append(items, item{creds.Candidate{Source: fmt.Sprintf("named token %q", e.Name), Kind: "named", Name: e.Name, Value: v, Expires: e.ExpiresAt()}, *e})
	} else {
		// Every available credential: named tokens, then the general chain.
		seen := map[string]bool{}
		for _, e := range ix.Tokens {
			c := creds.Candidate{Source: fmt.Sprintf("named token %q", e.Name), Kind: "named", Name: e.Name, Expires: e.ExpiresAt()}
			v, err := t.store.Get(e.Name)
			if err != nil {
				fmt.Fprintf(t.stdout, "%s: unavailable: %v\n\n", c.Source, err)
				continue
			}
			redact.AddSecret(v)
			c.Value = v
			seen[v] = true
			items = append(items, item{c, e})
		}
		// The general chain; named tokens were handled above.
		r := &creds.Resolver{Getenv: t.src.Creds.Getenv, GH: t.src.Creds.GH}
		for _, c := range r.Candidates("") {
			if c.Value == "" {
				if c.Err != creds.ErrUnset && c.Kind != "named" {
					fmt.Fprintf(t.stdout, "%s: unavailable: %v\n\n", c.Source, c.Err)
				}
				continue
			}
			if !seen[c.Value] {
				seen[c.Value] = true
				items = append(items, item{c, creds.Token{Type: creds.TokenType(c.Value)}})
			}
		}
		if len(items) == 0 {
			fmt.Fprintf(t.stdout, "no GitHub credential available.\n%s\n", creds.Order)
			return 1
		}
	}
	bad := 0
	for _, it := range items {
		fmt.Fprintf(t.stdout, "%s (%s):\n", it.c.Source, redact.Mask(it.c.Value))
		m, fail, err := t.src.Inspect(ctx, it.c)
		if err != nil {
			fmt.Fprintf(t.stdout, "  FAIL  GET /user: %v\n\n", redact.String(err.Error()))
			bad++
			continue
		}
		if fail != nil {
			fmt.Fprintf(t.stdout, "  FAIL  GET /user: %s\n        fix: %s\n\n", fail.Detail, fail.Fix)
			bad++
			logstore.Log(logstore.Entry{Event: logstore.EvTokenTest, Level: "warn", CredentialSource: it.c.Source, Result: "fail", HTTPStatus: fail.Status, ErrorCode: fail.Kind, Message: fail.Detail})
			continue
		}
		e := it.e
		if it.c.Kind == "named" {
			if x := ix.Find(e.Name); x != nil {
				x.Login, x.UserName, x.UserID, x.Scopes, x.LastChecked = m.Login, m.Name, m.ID, m.Scopes, t.now().UTC()
				x.Expires = ""
				if !m.Expires.IsZero() {
					x.Expires = m.Expires.UTC().Format(time.RFC3339)
				}
			}
		}
		fmt.Fprintf(t.stdout, "  PASS  GET /user\n")
		t.printMeta(e, m)
		e.Login = m.Login
		if n := t.printProbes(ctx, it.c, e); n > 0 {
			bad++
		}
		logstore.Log(logstore.Entry{Event: logstore.EvTokenTest, Level: "info", CredentialSource: it.c.Source, Result: "ok", Message: "login " + m.Login})
		fmt.Fprintln(t.stdout)
	}
	if len(ix.Tokens) > 0 {
		_ = ix.Save(t.indexPath) // refreshed metadata (no secrets)
	}
	// Which credential each mapped owner resolves to first.
	if owners := ix.Owners(); len(owners) > 0 && len(args) == 0 {
		fmt.Fprintln(t.stdout, "resolution (first available credential per owner):")
		for _, o := range owners {
			first := "none"
			if u := creds.Usable(t.src.Creds.Candidates(o)); len(u) > 0 {
				first = u[0].Source
			}
			fmt.Fprintf(t.stdout, "  @%s -> %s\n", o, first)
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func (t *tokenCmd) remove(args []string) int {
	if len(args) != 1 {
		return t.failf("usage: consona token remove <name>")
	}
	name := args[0]
	ix, err := creds.LoadIndex(t.indexPath)
	if err != nil {
		return t.failf("%v", err)
	}
	inIndex := false
	if e := ix.Find(name); e != nil {
		inIndex, name = true, e.Name
	}
	err = t.store.Delete(name)
	switch {
	case err == nil:
	case errors.Is(err, creds.ErrNoSecret):
		if !inIndex {
			return t.failf("no named token %q (see `consona token list`)", name)
		}
		fmt.Fprintf(t.stderr, "consona: note: %q had no value in the secure store\n", name)
	default:
		return t.failf("cannot remove %q from the secure store: %v", name, err)
	}
	if inIndex {
		ix.Remove(name)
		if err := ix.Save(t.indexPath); err != nil {
			return t.failf("saving the token index: %v", err)
		}
	}
	fmt.Fprintf(t.stdout, "consona: removed token %q (secure store and index). A running daemon stops using it within a minute.\n", name)
	logstore.Log(logstore.Entry{Event: logstore.EvTokenRemove, Level: "info", CredentialSource: fmt.Sprintf("named token %q", name), Result: "ok"})
	return 0
}

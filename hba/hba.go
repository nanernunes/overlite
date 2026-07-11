// Package hba implements PostgreSQL-style host-based authentication: an ordered
// set of rules that maps an incoming connection (type, database, user, address)
// to an authentication method (or a rejection). Two on-disk formats are
// supported through the Parser interface — the classic pg_hba.conf text format
// and a YAML equivalent — and Load picks between them (see load.go).
package hba

import (
	"net"
	"strings"
)

// Conn describes an incoming connection to be authorized.
type Conn struct {
	Local    bool   // Unix-socket connection (vs TCP)
	SSL      bool   // the connection was upgraded to TLS
	Database string // requested database
	User     string // requested role
	IP       net.IP // client address (nil for local connections)
}

// Rule is one HBA entry: a match on type/database/user/address that yields an
// authentication method.
type Rule struct {
	Type     string            // local | host | hostssl | hostnossl
	Database string            // all | name | csv | replication | sameuser | ...
	User     string            // all | name | csv | +group
	Address  string            // "" (local) | all | samehost | CIDR
	Method   string            // trust | reject | scram-sha-256 | md5 | password | peer | cert | ...
	Options  map[string]string // trailing key=value settings

	cidr *net.IPNet // parsed Address when it is a CIDR
}

// Policy is an ordered rule set.
type Policy struct {
	Rules []Rule
}

// Method returns the authentication method of the first rule that matches c,
// and true. When no rule matches it returns ("", false) — Postgres rejects such
// a connection.
func (p *Policy) Method(c Conn) (string, bool) {
	for i := range p.Rules {
		if p.Rules[i].matches(c) {
			return p.Rules[i].Method, true
		}
	}
	return "", false
}

func (r *Rule) matches(c Conn) bool {
	return r.matchType(c) &&
		matchDatabase(r.Database, c.Database) &&
		matchUser(r.User, c.User) &&
		r.matchAddr(c)
}

func (r *Rule) matchType(c Conn) bool {
	switch strings.ToLower(r.Type) {
	case "local":
		return c.Local
	case "host":
		return !c.Local
	case "hostssl":
		return !c.Local && c.SSL
	case "hostnossl":
		return !c.Local && !c.SSL
	}
	return false
}

func (r *Rule) matchAddr(c Conn) bool {
	if c.Local {
		return true // a local rule has no address column
	}
	switch strings.ToLower(strings.TrimSpace(r.Address)) {
	case "all":
		return true
	case "samehost":
		return c.IP != nil && c.IP.IsLoopback()
	case "":
		return false
	}
	if r.cidr != nil {
		return c.IP != nil && r.cidr.Contains(c.IP)
	}
	return false // hostnames aren't resolved
}

// matchDatabase matches an HBA database field (all / csv list / name). The
// pseudo-values that need connection context we don't have (replication,
// sameuser, ...) are treated as non-matching.
func matchDatabase(pattern, db string) bool {
	for _, p := range strings.Split(pattern, ",") {
		switch p = strings.TrimSpace(p); strings.ToLower(p) {
		case "all":
			return true
		case "replication", "sameuser", "samerole", "samegroup":
			continue
		default:
			if p == db {
				return true
			}
		}
	}
	return false
}

// matchUser matches an HBA user field (all / csv list / name / +group). Group
// membership isn't modeled, so "+ops" matches a user literally named "ops".
func matchUser(pattern, user string) bool {
	for _, p := range strings.Split(pattern, ",") {
		p = strings.TrimSpace(p)
		switch {
		case strings.EqualFold(p, "all"):
			return true
		case strings.HasPrefix(p, "+"):
			if strings.EqualFold(p[1:], user) {
				return true
			}
		case p == user:
			return true
		}
	}
	return false
}

// compile parses the Address into a CIDR when applicable.
func (r *Rule) compile() {
	if _, ipnet, err := net.ParseCIDR(r.Address); err == nil {
		r.cidr = ipnet
	}
}

package postgres

import (
	"strings"

	"overlite/core"
)

// SET / SHOW search_path are handled in the session (not the stateless utility
// interceptor) because the path is session state that also feeds unqualified
// name resolution in the engine.

// trySearchPath handles SET/SHOW/RESET search_path. It returns handled=true once
// it recognizes the statement; rs is a result set for SHOW, nil for SET/RESET.
func (s *session) trySearchPath(sql string) (rs *core.ResultSet, handled bool) {
	f := strings.Fields(sql)
	if len(f) < 2 {
		return nil, false
	}
	head := strings.ToUpper(f[0])
	// RESET search_path
	if head == "RESET" && strings.EqualFold(stripSemi(f[1]), "search_path") {
		s.searchPath = nil
		return nil, true
	}
	if head == "SHOW" && strings.EqualFold(stripSemi(f[1]), "search_path") {
		return &core.ResultSet{
			IsQuery:      true,
			Columns:      []core.Column{{Name: "search_path", DeclType: "text"}},
			Rows:         [][]core.Value{{s.searchPathString()}},
			RowsAffected: 1,
			Command:      "SHOW",
		}, true
	}
	// SET [SESSION|LOCAL] search_path (TO|=) <list>
	if head == "SET" {
		rest := f[1:]
		if len(rest) >= 1 && (strings.EqualFold(rest[0], "session") || strings.EqualFold(rest[0], "local")) {
			rest = rest[1:]
		}
		if len(rest) >= 2 && strings.EqualFold(rest[0], "search_path") &&
			(rest[1] == "=" || strings.EqualFold(rest[1], "to")) {
			s.searchPath = parseSearchPathList(strings.Join(rest[2:], " "))
			return nil, true
		}
	}
	return nil, false
}

// searchPathString renders the current path for SHOW (defaults like Postgres).
func (s *session) searchPathString() string {
	if len(s.searchPath) == 0 {
		return `"$user", public`
	}
	quoted := make([]string, len(s.searchPath))
	for i, p := range s.searchPath {
		if p == "$user" {
			quoted[i] = `"$user"`
		} else {
			quoted[i] = p
		}
	}
	return strings.Join(quoted, ", ")
}

// parseSearchPathList splits a search_path value into schema names, stripping
// quotes and a trailing semicolon. "DEFAULT" resets to the default (nil).
func parseSearchPathList(list string) []string {
	list = strings.TrimSpace(strings.TrimRight(list, "; \t"))
	if strings.EqualFold(list, "default") {
		return nil
	}
	var out []string
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stripSemi(s string) string { return strings.TrimRight(s, ";") }

// startupSearchPath extracts a search_path set at connection time: either the
// `search_path` startup parameter (drivers send the connection-string value) or
// `-c search_path=...` inside the `options` parameter (PGOPTIONS). Returns nil
// when none is given.
func startupSearchPath(params map[string]string) []string {
	if v, ok := params["search_path"]; ok && strings.TrimSpace(v) != "" {
		return parseSearchPathList(v)
	}
	opts := params["options"]
	if opts == "" {
		return nil
	}
	fields := strings.Fields(opts)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "-c" && i+1 < len(fields) {
			f = fields[i+1]
			i++
		} else {
			f = strings.TrimPrefix(f, "-c")
		}
		if kv := strings.SplitN(f, "=", 2); len(kv) == 2 && strings.EqualFold(kv[0], "search_path") {
			return parseSearchPathList(kv[1])
		}
	}
	return nil
}

// isSearchPathStmt reports whether raw is a SET/SHOW/RESET search_path statement
// (routed specially in the extended path so it updates session state).
func isSearchPathStmt(raw string) bool {
	f := strings.Fields(raw)
	if len(f) < 2 {
		return false
	}
	switch strings.ToUpper(f[0]) {
	case "RESET", "SHOW":
		return strings.EqualFold(stripSemi(f[1]), "search_path")
	case "SET":
		rest := f[1:]
		if len(rest) >= 1 && (strings.EqualFold(rest[0], "session") || strings.EqualFold(rest[0], "local")) {
			rest = rest[1:]
		}
		return len(rest) >= 1 && strings.EqualFold(rest[0], "search_path")
	}
	return false
}

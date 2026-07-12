package postgres

import (
	"strings"
	"sync"
)

// LISTEN/NOTIFY: overlite is a single process, so notifications are delivered
// in-memory to every session on the same server that LISTENs on the channel.
// A session's connection is written by its own goroutine (responses) and, when
// the session is idle, by a NOTIFYing goroutine. The two never overlap: a
// session brackets its response with goBusy()/goIdle() (both take s.nmu), and a
// NOTIFY only writes while the session is idle (also under s.nmu) — so s.nmu
// orders every write to the connection, and a notification lands at a message
// boundary.

type pgNotify struct {
	pid     int32
	channel string
	payload string
}

// notifyRegistry maps a channel to the sessions listening on it.
var notifyRegistry = struct {
	mu       sync.Mutex
	channels map[string]map[*session]bool
}{channels: map[string]map[*session]bool{}}

func registerListen(ch string, s *session) {
	notifyRegistry.mu.Lock()
	defer notifyRegistry.mu.Unlock()
	if notifyRegistry.channels[ch] == nil {
		notifyRegistry.channels[ch] = map[*session]bool{}
	}
	notifyRegistry.channels[ch][s] = true
}

func unregisterListen(ch string, s *session) {
	notifyRegistry.mu.Lock()
	defer notifyRegistry.mu.Unlock()
	if m := notifyRegistry.channels[ch]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(notifyRegistry.channels, ch)
		}
	}
}

func unregisterAll(s *session) {
	notifyRegistry.mu.Lock()
	defer notifyRegistry.mu.Unlock()
	for ch, m := range notifyRegistry.channels {
		delete(m, s)
		if len(m) == 0 {
			delete(notifyRegistry.channels, ch)
		}
	}
}

// publishNotify delivers a notification to every session listening on channel.
func publishNotify(channel, payload string, pid int32) {
	notifyRegistry.mu.Lock()
	var targets []*session
	for s := range notifyRegistry.channels[channel] {
		targets = append(targets, s)
	}
	notifyRegistry.mu.Unlock()
	for _, s := range targets {
		s.enqueueNotify(pgNotify{pid: pid, channel: channel, payload: payload})
	}
}

// --- session side -----------------------------------------------------------

// goBusy / goIdle bracket a session's write region; when idle, a NOTIFY may
// write to the connection.
func (s *session) goBusy() {
	s.nmu.Lock()
	s.idle = false
	s.nmu.Unlock()
}

func (s *session) goIdle() {
	s.nmu.Lock()
	s.idle = true
	s.deliverLocked()
	s.nmu.Unlock()
}

func (s *session) enqueueNotify(n pgNotify) {
	s.nmu.Lock()
	s.pending = append(s.pending, n)
	if s.idle {
		s.deliverLocked()
	}
	s.nmu.Unlock()
}

// deliverLocked writes and flushes any pending notifications; caller holds nmu.
func (s *session) deliverLocked() {
	if len(s.pending) == 0 {
		return
	}
	for _, n := range s.pending {
		var b []byte
		b = appendCString(i32(n.pid), n.channel)
		b = appendCString(b, n.payload)
		_ = s.c.send(msgNotification, b)
	}
	s.pending = nil
	_ = s.c.flush()
}

// isListenNotify reports whether sql is a LISTEN/UNLISTEN/NOTIFY statement.
func isListenNotify(sql string) bool {
	switch firstWordUpper(sql) {
	case "LISTEN", "UNLISTEN", "NOTIFY":
		return true
	}
	return false
}

// tryListenNotify handles LISTEN / UNLISTEN / NOTIFY, returning handled=false for
// anything else.
func (s *session) tryListenNotify(sql string) (string, bool) {
	f := strings.Fields(sql)
	if len(f) == 0 {
		return "", false
	}
	switch strings.ToUpper(strings.TrimRight(f[0], ";")) {
	case "LISTEN":
		if ch := notifyChannel(f, 1); ch != "" {
			s.listens[ch] = true
			registerListen(ch, s)
		}
		return "LISTEN", true
	case "UNLISTEN":
		arg := ""
		if len(f) > 1 {
			arg = strings.TrimRight(f[1], ";")
		}
		if arg == "*" {
			for ch := range s.listens {
				unregisterListen(ch, s)
			}
			s.listens = map[string]bool{}
		} else if ch := notifyChannel(f, 1); ch != "" {
			delete(s.listens, ch)
			unregisterListen(ch, s)
		}
		return "UNLISTEN", true
	case "NOTIFY":
		ch, payload := parseNotify(sql)
		if ch != "" {
			publishNotify(ch, payload, s.pid)
		}
		return "NOTIFY", true
	}
	return "", false
}

// notifyChannel returns the (unquoted) channel identifier at position i.
func notifyChannel(f []string, i int) string {
	if i >= len(f) {
		return ""
	}
	return unquoteIdent(strings.TrimRight(f[i], ",;"))
}

// parseNotify extracts the channel and optional 'payload' from a NOTIFY.
func parseNotify(sql string) (channel, payload string) {
	rest := strings.TrimSpace(sql[indexWord(strings.ToLower(sql), "notify")+len("notify"):])
	comma := -1
	depth := 0
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\'' {
			i = endOfStringLiteral(rest, i) - 1
			continue
		}
		if rest[i] == '(' {
			depth++
		} else if rest[i] == ')' {
			depth--
		} else if rest[i] == ',' && depth == 0 {
			comma = i
			break
		}
	}
	if comma < 0 {
		return unquoteIdent(strings.TrimRight(strings.TrimSpace(rest), ";")), ""
	}
	channel = unquoteIdent(strings.TrimSpace(rest[:comma]))
	p := strings.TrimSpace(strings.TrimRight(rest[comma+1:], ";"))
	if len(p) >= 2 && p[0] == '\'' {
		payload = strings.ReplaceAll(p[1:len(p)-1], "''", "'")
	}
	return channel, payload
}

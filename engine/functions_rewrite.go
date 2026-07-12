package engine

import "strings"

// rewriteSQLFunctions inlines calls to stored LANGUAGE sql functions. A call
// f(x, y) becomes "(<body with x,y substituted>)". It loops so a function whose
// body calls another function is fully expanded; a small cap guards recursion.
func rewriteSQLFunctions(query string) string {
	if !haveFunctions() || !strings.Contains(query, "(") {
		return query
	}
	for i := 0; i < 16; i++ {
		next, changed := inlineFunctionsOnce(query)
		if !changed {
			return next
		}
		query = next
	}
	return query
}

func inlineFunctionsOnce(sql string) (string, bool) {
	var b strings.Builder
	changed := false
	i := 0
	for i < len(sql) {
		c := sql[i]
		if c == '\'' || c == '"' {
			j := skipLiteral(sql, i, c)
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if isIdentByte(c) && (i == 0 || !isIdentByte(sql[i-1])) {
			j := i
			for j < len(sql) && isIdentByte(sql[j]) {
				j++
			}
			word := sql[i:j]
			k := skipSpaces2(sql, j)
			if k < len(sql) && sql[k] == '(' && anyFuncNamed(word) {
				if args, end, ok := balancedArgs(sql, k); ok {
					parts := splitTopLevelArgs(args)
					if fn, found := lookupFunc(word, len(parts)); found {
						b.WriteString("(" + substituteBody(fn, parts) + ")")
						i = end
						changed = true
						continue
					}
				}
			}
			b.WriteString(word)
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), changed
}

// substituteBody replaces the function's parameters in its body with the call
// arguments: $1..$n positionally and each named parameter by name, each wrapped
// in parentheses to preserve precedence.
func substituteBody(fn sqlFunc, args []string) string {
	wrapped := make([]string, len(args))
	for i, a := range args {
		wrapped[i] = "(" + strings.TrimSpace(a) + ")"
	}
	byName := map[string]string{}
	for i, name := range fn.params {
		if name != "" && i < len(wrapped) {
			byName[strings.ToLower(name)] = wrapped[i]
		}
	}

	var b strings.Builder
	s := fn.body
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\'' || c == '"' {
			j := skipLiteral(s, i, c)
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if c == '$' && i+1 < len(s) && s[i+1] >= '1' && s[i+1] <= '9' {
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if n := atoiSafe(s[i+1 : j]); n >= 1 && n <= len(wrapped) {
				b.WriteString(wrapped[n-1])
				i = j
				continue
			}
		}
		if isIdentByte(c) && (i == 0 || !isIdentByte(s[i-1])) {
			j := i
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			word := s[i:j]
			if repl, ok := byName[strings.ToLower(word)]; ok {
				b.WriteString(repl)
				i = j
				continue
			}
			b.WriteString(word)
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// balancedArgs returns the text between the parenthesis at open and its match,
// and the index just past the closing parenthesis.
func balancedArgs(s string, open int) (args string, end int, ok bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			i = skipLiteral(s, i, s[i]) - 1
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i + 1, true
			}
		}
	}
	return "", 0, false
}

// splitTopLevelArgs splits on commas outside parentheses/strings. An all-blank
// input yields no parts (a zero-arg call).
func splitTopLevelArgs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			i = skipLiteral(s, i, s[i]) - 1
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func atoiSafe(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

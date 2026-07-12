package engine

import "strings"

// parseCreateFunction parses a CREATE FUNCTION into a sqlFunc (name, param names,
// arity, body, language). It handles a dollar-quoted or single-quoted body and
// the two keyword orders (AS … LANGUAGE … / LANGUAGE … AS …).
func parseCreateFunction(sql string) (sqlFunc, bool) {
	var fn sqlFunc
	low := strings.ToLower(sql)
	fi := indexWord(low, "function")
	if fi < 0 {
		return fn, false
	}
	// name: after FUNCTION up to '(' or whitespace.
	p := skipSpaces2(sql, fi+len("function"))
	ns := p
	for p < len(sql) && sql[p] != '(' && !isSpace2(sql[p]) {
		p++
	}
	fn.name = strings.Trim(strings.TrimSpace(sql[ns:p]), `"`)
	if d := strings.LastIndexByte(fn.name, '.'); d >= 0 {
		fn.name = fn.name[d+1:] // drop a schema qualifier
	}
	if fn.name == "" {
		return fn, false
	}
	// params: the first parenthesized group after the name.
	op := indexByteFrom(sql, '(', p)
	if op < 0 {
		return fn, false
	}
	params, _, ok := balancedArgs(sql, op)
	if !ok {
		return fn, false
	}
	fn.params, fn.arity = parseParams(params)

	// body: dollar-quoted or single-quoted.
	body, bs, be, ok := findDollarBody(sql)
	if !ok {
		body, bs, be, ok = findQuotedBody(sql)
	}
	if !ok {
		return fn, false
	}
	fn.body = strings.TrimSpace(body)

	// language: search outside the body region (default order puts it after).
	region := sql[:bs] + " " + sql[be:]
	fn.lang = wordAfterKeyword(region, "language")
	return fn, true
}

// parseDropFunction extracts the name and (if given) arity from DROP FUNCTION.
// arity is -1 when no argument list is present.
func parseDropFunction(sql string) (name string, arity int, ok bool) {
	low := strings.ToLower(sql)
	fi := indexWord(low, "function")
	if fi < 0 {
		return "", 0, false
	}
	p := skipSpaces2(sql, fi+len("function"))
	if strings.HasPrefix(low[p:], "if exists") {
		p = skipSpaces2(sql, p+len("if exists"))
	}
	ns := p
	for p < len(sql) && sql[p] != '(' && !isSpace2(sql[p]) && sql[p] != ';' && sql[p] != ',' {
		p++
	}
	name = strings.Trim(strings.TrimSpace(sql[ns:p]), `"`)
	if d := strings.LastIndexByte(name, '.'); d >= 0 {
		name = name[d+1:]
	}
	if name == "" {
		return "", 0, false
	}
	ar := -1
	if op := indexByteFrom(sql, '(', p); op >= 0 && op == skipSpaces2(sql, p) {
		if args, _, k := balancedArgs(sql, op); k {
			_, ar = parseParams(args)
		}
	}
	return name, ar, true
}

// parseParams splits a parameter list into per-slot names ("" when unnamed) and
// the arity.
func parseParams(params string) ([]string, int) {
	if strings.TrimSpace(params) == "" {
		return nil, 0
	}
	parts := splitTopLevelArgs(params)
	names := make([]string, len(parts))
	for i, part := range parts {
		toks := strings.Fields(strings.TrimSpace(part))
		if len(toks) > 0 {
			switch strings.ToLower(toks[0]) {
			case "in", "out", "inout", "variadic":
				toks = toks[1:]
			}
		}
		if len(toks) >= 2 { // "<name> <type> …" → the first token is the name
			names[i] = strings.Trim(toks[0], `"`)
		}
	}
	return names, len(parts)
}

// findDollarBody returns the body of a $tag$…$tag$ quoted section and its span.
func findDollarBody(sql string) (body string, start, end int, ok bool) {
	for i := 0; i < len(sql); i++ {
		if sql[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(sql) && isIdentByte(sql[j]) {
			j++
		}
		if j >= len(sql) || sql[j] != '$' {
			continue
		}
		tag := sql[i : j+1] // "$$" or "$name$"
		if k := strings.Index(sql[j+1:], tag); k >= 0 {
			return sql[j+1 : j+1+k], i, j + 1 + k + len(tag), true
		}
	}
	return "", 0, 0, false
}

// findQuotedBody returns the body of an `AS '…'` clause and its span.
func findQuotedBody(sql string) (body string, start, end int, ok bool) {
	low := strings.ToLower(sql)
	from := 0
	for {
		a := indexWord(low[from:], "as")
		if a < 0 {
			return "", 0, 0, false
		}
		a += from
		q := skipSpaces2(sql, a+2)
		if q < len(sql) && sql[q] == '\'' {
			e := skipLiteral(sql, q, '\'')
			return strings.ReplaceAll(sql[q+1:e-1], "''", "'"), q, e, true
		}
		from = a + 2
	}
}

// wordAfterKeyword returns the token following a whole-word keyword.
func wordAfterKeyword(s, keyword string) string {
	low := strings.ToLower(s)
	i := indexWord(low, keyword)
	if i < 0 {
		return ""
	}
	p := skipSpaces2(s, i+len(keyword))
	start := p
	for p < len(s) && (isIdentByte(s[p])) {
		p++
	}
	return strings.ToLower(s[start:p])
}

// indexWord finds a whole-word (identifier-bounded) occurrence of word in low.
func indexWord(low, word string) int {
	from := 0
	for {
		i := strings.Index(low[from:], word)
		if i < 0 {
			return -1
		}
		i += from
		before := i == 0 || !isIdentByte(low[i-1])
		after := i+len(word) >= len(low) || !isIdentByte(low[i+len(word)])
		if before && after {
			return i
		}
		from = i + len(word)
	}
}

func indexByteFrom(s string, b byte, from int) int {
	if from < 0 {
		from = 0
	}
	if i := strings.IndexByte(s[from:], b); i >= 0 {
		return from + i
	}
	return -1
}

func skipSpaces2(s string, i int) int {
	for i < len(s) && isSpace2(s[i]) {
		i++
	}
	return i
}

func isSpace2(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

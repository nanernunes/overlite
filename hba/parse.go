package hba

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parser reads an HBA Policy from one on-disk format. ConfParser and YAMLParser
// are the two implementations.
type Parser interface {
	Parse(r io.Reader) (*Policy, error)
}

// ConfParser parses the classic pg_hba.conf text format: one rule per line,
// whitespace-separated columns, '#' comments. A `local` line omits the address
// column.
type ConfParser struct{}

func (ConfParser) Parse(r io.Reader) (*Policy, error) {
	var p Policy
	sc := bufio.NewScanner(r)
	for line := 0; sc.Scan(); {
		line++
		text := sc.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		toks := tokenize(text)
		if len(toks) == 0 {
			continue
		}
		rule, err := confRule(toks)
		if err != nil {
			return nil, fmt.Errorf("pg_hba.conf line %d: %w", line, err)
		}
		rule.compile()
		p.Rules = append(p.Rules, rule)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &p, nil
}

func confRule(toks []string) (Rule, error) {
	r := Rule{Type: strings.ToLower(toks[0])}
	var rest []string
	if r.Type == "local" { // type db user method [options]
		if len(toks) < 4 {
			return r, fmt.Errorf("local rule needs database, user, method")
		}
		r.Database, r.User, r.Method, rest = toks[1], toks[2], toks[3], toks[4:]
	} else { // type db user address method [options]
		if len(toks) < 5 {
			return r, fmt.Errorf("%s rule needs database, user, address, method", r.Type)
		}
		r.Database, r.User, r.Address, r.Method, rest = toks[1], toks[2], toks[3], toks[4], toks[5:]
	}
	r.Options = parseOptions(rest)
	return r, nil
}

// tokenize splits a line on whitespace, honoring "double quoted" tokens.
func tokenize(line string) []string {
	var toks []string
	for i := 0; i < len(line); {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '\r') {
			i++
		}
		if i >= len(line) {
			break
		}
		if line[i] == '"' {
			j := i + 1
			for j < len(line) && line[j] != '"' {
				j++
			}
			toks = append(toks, line[i+1:j])
			i = j + 1
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' && line[j] != '\t' && line[j] != '\r' {
			j++
		}
		toks = append(toks, line[i:j])
		i = j
	}
	return toks
}

func parseOptions(toks []string) map[string]string {
	if len(toks) == 0 {
		return nil
	}
	m := make(map[string]string, len(toks))
	for _, t := range toks {
		if i := strings.IndexByte(t, '='); i > 0 {
			m[t[:i]] = strings.Trim(t[i+1:], `"`)
		}
	}
	return m
}

// YAMLParser parses the YAML representation: a top-level `hba:` list of rules
// with type/database/user/address/method fields and an optional options map.
type YAMLParser struct{}

func (YAMLParser) Parse(r io.Reader) (*Policy, error) {
	var doc struct {
		HBA []struct {
			Type     string            `yaml:"type"`
			Database string            `yaml:"database"`
			User     string            `yaml:"user"`
			Address  string            `yaml:"address"`
			Method   string            `yaml:"method"`
			Options  map[string]string `yaml:"options"`
		} `yaml:"hba"`
	}
	if err := yaml.NewDecoder(r).Decode(&doc); err != nil && err != io.EOF {
		return nil, err
	}
	var p Policy
	for _, y := range doc.HBA {
		rule := Rule{
			Type:     strings.ToLower(y.Type),
			Database: orDefault(y.Database, "all"),
			User:     orDefault(y.User, "all"),
			Address:  y.Address,
			Method:   y.Method,
			Options:  y.Options,
		}
		rule.compile()
		p.Rules = append(p.Rules, rule)
	}
	return &p, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

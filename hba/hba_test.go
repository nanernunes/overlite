package hba

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConf(t *testing.T) {
	p, err := ConfParser{}.Parse(strings.NewReader(`
# a comment
local   all       all                        trust
host    all       all       127.0.0.1/32     scram-sha-256
hostssl shop      appuser   10.0.0.0/8       md5           clientcert=verify-full
host    all       all       0.0.0.0/0        reject
`))
	require.NoError(t, err)
	require.Len(t, p.Rules, 4)

	assert.Equal(t, "local", p.Rules[0].Type)
	assert.Equal(t, "trust", p.Rules[0].Method)
	assert.Equal(t, "scram-sha-256", p.Rules[1].Method)
	assert.Equal(t, "10.0.0.0/8", p.Rules[2].Address)
	assert.Equal(t, "verify-full", p.Rules[2].Options["clientcert"])
	assert.Equal(t, "reject", p.Rules[3].Method)
}

func TestParseYAML(t *testing.T) {
	p, err := YAMLParser{}.Parse(strings.NewReader(`
hba:
  - { type: local, database: all, user: all, method: trust }
  - type: host
    database: all
    user: all
    address: 127.0.0.1/32
    method: scram-sha-256
  - type: hostssl
    database: shop
    user: appuser
    address: 10.0.0.0/8
    method: md5
    options:
      clientcert: verify-full
  - type: host
    database: all
    user: all
    address: 0.0.0.0/0
    method: reject
`))
	require.NoError(t, err)
	require.Len(t, p.Rules, 4)
	assert.Equal(t, "trust", p.Rules[0].Method)
	assert.Equal(t, "scram-sha-256", p.Rules[1].Method)
	assert.Equal(t, "verify-full", p.Rules[2].Options["clientcert"])
	assert.Equal(t, "reject", p.Rules[3].Method)
}

func TestMatchFirstWins(t *testing.T) {
	p, err := ConfParser{}.Parse(strings.NewReader(`
host    all    all      127.0.0.1/32    trust
host    shop   app      10.0.0.0/8      md5
hostssl all    all      0.0.0.0/0       scram-sha-256
host    all    all      0.0.0.0/0       reject
`))
	require.NoError(t, err)

	// Loopback -> trust (first rule).
	m, ok := p.Method(Conn{IP: net.ParseIP("127.0.0.1"), Database: "x", User: "y"})
	assert.True(t, ok)
	assert.Equal(t, "trust", m)

	// app@shop from 10.x -> md5.
	m, _ = p.Method(Conn{IP: net.ParseIP("10.1.2.3"), Database: "shop", User: "app"})
	assert.Equal(t, "md5", m)

	// bob@shop from 10.x without TLS: md5 rule needs user 'app', hostssl needs
	// TLS, so it falls through to the final reject.
	m, _ = p.Method(Conn{IP: net.ParseIP("10.1.2.3"), Database: "shop", User: "bob", SSL: false})
	assert.Equal(t, "reject", m)

	// Same but over TLS -> the hostssl scram rule matches.
	m, _ = p.Method(Conn{IP: net.ParseIP("10.1.2.3"), Database: "shop", User: "bob", SSL: true})
	assert.Equal(t, "scram-sha-256", m)
}

func TestNoMatch(t *testing.T) {
	p, err := ConfParser{}.Parse(strings.NewReader("host all all 127.0.0.1/32 trust\n"))
	require.NoError(t, err)
	_, ok := p.Method(Conn{IP: net.ParseIP("8.8.8.8"), Database: "x", User: "y"})
	assert.False(t, ok, "an unmatched connection reports no rule")
}

func TestLocalAndGroupAndCSV(t *testing.T) {
	p, err := ConfParser{}.Parse(strings.NewReader(`
local   all              all                       peer
host    db1,db2          +ops     10.0.0.0/8       md5
`))
	require.NoError(t, err)

	m, ok := p.Method(Conn{Local: true, Database: "anything", User: "root"})
	assert.True(t, ok)
	assert.Equal(t, "peer", m)

	// db in the CSV list + member of +ops (name match) + in CIDR.
	m, ok = p.Method(Conn{IP: net.ParseIP("10.9.9.9"), Database: "db2", User: "ops"})
	assert.True(t, ok)
	assert.Equal(t, "md5", m)

	// db3 not listed -> no match.
	_, ok = p.Method(Conn{IP: net.ParseIP("10.9.9.9"), Database: "db3", User: "ops"})
	assert.False(t, ok)
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	writeYAML := func() {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "pg_hba.yaml"),
			[]byte("hba:\n  - {type: host, database: all, user: all, address: 0.0.0.0/0, method: md5}\n"), 0o644))
	}

	// Only YAML present -> YAML is used.
	writeYAML()
	p, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, p)
	m, _ := p.Method(Conn{IP: net.ParseIP("1.2.3.4"), Database: "d", User: "u"})
	assert.Equal(t, "md5", m)

	// Both present -> .conf wins.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pg_hba.conf"),
		[]byte("host all all 0.0.0.0/0 trust\n"), 0o644))
	p, err = Load(dir)
	require.NoError(t, err)
	m, _ = p.Method(Conn{IP: net.ParseIP("1.2.3.4"), Database: "d", User: "u"})
	assert.Equal(t, "trust", m, "pg_hba.conf takes precedence over pg_hba.yaml")

	// Neither present -> nil policy (no HBA configured).
	p, err = Load(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, p)
}

// The two format parsers satisfy the same interface.
var _ Parser = ConfParser{}
var _ Parser = YAMLParser{}

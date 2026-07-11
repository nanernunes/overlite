package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnumTypeLifecycle(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')`)

	// The type and its labels show up in pg_type / pg_enum, in order.
	assert.Contains(t, queryColumn(t, conn,
		`SELECT typname FROM pg_catalog.pg_type WHERE typtype = 'e'`, 0), "mood")
	assert.Equal(t, []string{"sad", "ok", "happy"}, queryColumn(t, conn,
		`SELECT enumlabel FROM pg_catalog.pg_enum e
		 JOIN pg_catalog.pg_type t ON t.oid = e.enumtypid
		 WHERE t.typname = 'mood' ORDER BY e.enumsortorder`, 0))

	// A column of the enum type accepts a valid label and rejects an invalid one.
	mustExec(t, conn, `CREATE TABLE person (name text, current_mood mood NOT NULL DEFAULT 'ok')`)
	mustExec(t, conn, `INSERT INTO person (name, current_mood) VALUES ('ada', 'happy')`)
	_, err := conn.Exec(ctx, `INSERT INTO person (name, current_mood) VALUES ('bob', 'furious')`)
	require.Error(t, err, "value outside the enum must be rejected by the CHECK")

	// The DEFAULT applies and the stored value is the plain label text.
	mustExec(t, conn, `INSERT INTO person (name) VALUES ('cleo')`)
	assert.Equal(t, []string{"happy", "ok"}, queryColumn(t, conn,
		`SELECT current_mood FROM person ORDER BY name`, 0))

	// ALTER TYPE ADD VALUE (append and positioned).
	mustExec(t, conn, `ALTER TYPE mood ADD VALUE 'excited'`)
	mustExec(t, conn, `ALTER TYPE mood ADD VALUE 'meh' BEFORE 'ok'`)
	assert.Equal(t, []string{"sad", "meh", "ok", "happy", "excited"}, queryColumn(t, conn,
		`SELECT enumlabel FROM pg_catalog.pg_enum e
		 JOIN pg_catalog.pg_type t ON t.oid = e.enumtypid
		 WHERE t.typname = 'mood' ORDER BY e.enumsortorder`, 0))

	// A table created after the ALTER accepts the new label.
	mustExec(t, conn, `CREATE TABLE evt (m mood)`)
	mustExec(t, conn, `INSERT INTO evt VALUES ('excited')`)

	// DROP TYPE removes it from the catalog.
	mustExec(t, conn, `DROP TYPE mood`)
	assert.NotContains(t, queryColumn(t, conn,
		`SELECT typname FROM pg_catalog.pg_type WHERE typtype = 'e'`, 0), "mood")

	// Internal enum tables stay hidden from the catalog.
	for _, rel := range queryColumn(t, conn,
		`SELECT relname FROM pg_catalog.pg_class WHERE relkind = 'r'`, 0) {
		assert.False(t, strings.HasPrefix(rel, "_overlite"), "internal table %q leaked", rel)
	}
}

// TestEnumExtendedProtocol exercises the Parse/Bind/Execute path (JDBC/DBeaver).
func TestEnumExtendedProtocol(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TYPE color AS ENUM ('red', 'green', 'blue')`)
	mustExec(t, conn, `CREATE TABLE paint (c color)`)
	_, err := conn.Exec(ctx, `INSERT INTO paint (c) VALUES ($1)`, "green")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO paint (c) VALUES ($1)`, "purple")
	require.Error(t, err, "invalid enum label must be rejected")

	assert.Equal(t, []string{"green"}, queryColumn(t, conn, `SELECT c FROM paint`, 0))
}

func TestNonEnumCreateTypeIsAccepted(t *testing.T) {
	conn := connect(t, startServer(t))
	// Composite/other CREATE TYPE forms are accepted as a no-op (not modeled).
	mustExec(t, conn, `CREATE TYPE addr AS (street text, zip text)`)
}

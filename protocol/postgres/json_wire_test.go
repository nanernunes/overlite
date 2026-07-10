package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// json/jsonb columns, the -> / ->> / #> operators, and the builder/aggregate
// functions, end to end.
func TestJSONBasics(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE docs (id SERIAL PRIMARY KEY, data JSONB)`)
	mustExec(t, conn, `INSERT INTO docs (data) VALUES ('{"nome":"ana","tags":["x","y"],"end":{"cidade":"SP"}}')`)

	// ->> returns text; -> returns JSON (quoted).
	var nome, nomeJSON string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT data ->> 'nome', data -> 'nome' FROM docs`).Scan(&nome, &nomeJSON))
	assert.Equal(t, "ana", nome)
	assert.Equal(t, `"ana"`, nomeJSON)

	// Array element and nested path via #>>.
	var tag0, cidade string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT data -> 'tags' ->> 0, data #>> '{end,cidade}' FROM docs`).Scan(&tag0, &cidade))
	assert.Equal(t, "x", tag0)
	assert.Equal(t, "SP", cidade)

	// Filter using a JSON extraction.
	var count int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM docs WHERE data ->> 'nome' = 'ana'`).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestJSONBuilders(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	var obj, arr string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT jsonb_build_object('a', 1, 'b', 'x'), json_build_array(1, 2, 3)`).Scan(&obj, &arr))
	assert.JSONEq(t, `{"a":1,"b":"x"}`, obj)
	assert.Equal(t, "[1,2,3]", arr)

	mustExec(t, conn, `CREATE TABLE nums (n INTEGER)`)
	mustExec(t, conn, `INSERT INTO nums VALUES (1), (2), (3)`)
	var agg string
	require.NoError(t, conn.QueryRow(ctx, `SELECT jsonb_agg(n) FROM nums`).Scan(&agg))
	assert.Equal(t, "[1,2,3]", agg)
}

func TestJSONCatalogType(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE docs (id INTEGER, data JSONB, meta JSON)`)

	typeOf := func(col string) string {
		var typ string
		require.NoError(t, conn.QueryRow(context.Background(), `
			SELECT format_type(a.atttypid, a.atttypmod)
			FROM pg_catalog.pg_attribute a
			JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
			WHERE c.relname = 'docs' AND a.attname = $1`, col).Scan(&typ))
		return typ
	}
	assert.Equal(t, "jsonb", typeOf("data"))
	assert.Equal(t, "json", typeOf("meta"))
}

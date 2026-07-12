package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFunctionIntrospection: a user LANGUAGE sql function appears in pg_proc and
// its definition renders via pg_get_functiondef / _arguments / _result (what
// \df, \sf, and pg_dump read).
func TestFunctionIntrospection(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx,
		`CREATE FUNCTION add_tax(price numeric, rate numeric) RETURNS numeric
		 AS $$ SELECT price * (1 + rate) $$ LANGUAGE sql`)
	require.NoError(t, err)

	// It's listed in pg_proc under public with the right arity.
	var nargs int
	var lang string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT p.pronargs, l.lanname
		FROM pg_proc p JOIN pg_language l ON l.oid = p.prolang
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE p.proname = 'add_tax' AND n.nspname = 'public'`).Scan(&nargs, &lang))
	assert.Equal(t, 2, nargs)
	assert.Equal(t, "sql", lang)

	// The definition renderers return the real signature and body.
	var args, result, def string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT pg_get_function_arguments(p.oid), pg_get_function_result(p.oid), pg_get_functiondef(p.oid)
		FROM pg_proc p WHERE p.proname = 'add_tax'`).Scan(&args, &result, &def))
	assert.Equal(t, "price numeric, rate numeric", args)
	assert.Equal(t, "numeric", result)
	assert.Contains(t, def, "CREATE FUNCTION")
	assert.Contains(t, def, "SELECT price * (1 + rate)")

	// pg_get_expr returns a stored expression verbatim (column default text).
	_, err = conn.Exec(ctx, `CREATE TABLE d (id int DEFAULT 42)`)
	require.NoError(t, err)
	var expr string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT pg_get_expr(a.adbin, a.adrelid) FROM pg_attrdef a
		JOIN pg_class c ON c.oid = a.adrelid WHERE c.relname = 'd'`).Scan(&expr))
	assert.Equal(t, "42", expr)
}

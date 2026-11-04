package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Postgres' regular expressions have word and string anchors Go's do not
// spell the same: \m and \M (start and end of a word), \y and \Y (a word
// boundary, or not one) and \Z (end of string). Each was a parse error.
func TestRegexEscapes(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	for _, c := range []struct {
		pattern, subject string
		match            bool
	}{
		{`\m(cds?|lps?)\M`, "cds, dvds e lps", true},
		{`\mlp\M`, "help", false}, // inside a word
		{`\mlp\M`, "lps", false},  // not the end of a word
		{`\mvinil`, "vinis e vinil", true},
		{`vinil\M`, "vinilico", false},
		{`a\mb`, "a b", false},  // \m needs a word character after it
		{`a\m b`, "a b", false}, // ...and none before it
		{`\M `, "lp ", true},
		{`\M`, "", false},
		{`\ylp\y`, "um lp.", true},
		{`l\Yp`, "lp", true},
		{`l\Yp`, "l p", false},
		{`fim\Z`, "o fim", true},
		{`fim\Z`, "fim dela", false},
		{`\Aum`, "um lp", true},
		{`\\m`, `a\m`, true},            // an escaped backslash stays a backslash
		{`\mvinil\M`, "vinilão", false}, // accented letters are word characters
		{`ão\M`, "vinilão", true},
	} {
		var got int
		require.NoError(t, conn.QueryRow(ctx, `SELECT CASE WHEN $1::text ~ $2::text THEN 1 ELSE 0 END`,
			c.subject, c.pattern).Scan(&got), "%s ~ %s", c.subject, c.pattern)
		assert.Equal(t, c.match, got == 1, "%q ~ %q", c.subject, c.pattern)
	}

	// Case-insensitive, negated and in a WHERE over a table.
	mustExec(t, conn, `CREATE TABLE d (s text)`)
	mustExec(t, conn, `INSERT INTO d VALUES ('Discos de VINIL'), ('vinilico'), ('livros')`)
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM d WHERE s ~* '\mvinil\M'`).Scan(&n))
	assert.Equal(t, 1, n)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM d WHERE s !~* '\mvinil\M'`).Scan(&n))
	assert.Equal(t, 2, n)

}

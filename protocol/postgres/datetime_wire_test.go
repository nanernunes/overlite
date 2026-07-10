package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercises the date/time surface end to end: timestamp storage, now(),
// current_date, date_trunc, extract/date_part, and to_char.
func TestDateTimeFunctions(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// timestamp/date columns store and round-trip.
	mustExec(t, conn, `CREATE TABLE eventos (id SERIAL PRIMARY KEY, quando TIMESTAMP, dia DATE)`)
	mustExec(t, conn, `INSERT INTO eventos (quando, dia) VALUES ('2024-03-15 14:30:45', '2024-03-15')`)

	var quando, dia string
	require.NoError(t, conn.QueryRow(ctx, `SELECT quando, dia FROM eventos`).Scan(&quando, &dia))
	assert.Equal(t, "2024-03-15 14:30:45", quando)
	assert.Equal(t, "2024-03-15", dia)

	// date_trunc.
	var truncMonth string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT date_trunc('month', quando) FROM eventos`).Scan(&truncMonth))
	assert.Equal(t, "2024-03-01 00:00:00", truncMonth)

	// extract / date_part.
	var year, month, day int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT extract(year FROM quando), extract(month FROM quando), date_part('day', quando) FROM eventos`).
		Scan(&year, &month, &day))
	assert.Equal(t, [3]int{2024, 3, 15}, [3]int{year, month, day})

	// to_char.
	var formatted string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT to_char(quando, 'YYYY-MM-DD') FROM eventos`).Scan(&formatted))
	assert.Equal(t, "2024-03-15", formatted)

	// now() and current_date resolve to a real timestamp/date.
	var nowStr, today string
	require.NoError(t, conn.QueryRow(ctx, `SELECT now(), current_date`).Scan(&nowStr, &today))
	assert.Contains(t, nowStr, time.Now().UTC().Format("2006-01-02"))
	assert.Equal(t, time.Now().UTC().Format("2006-01-02"), today)
}

package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestToCharTokens(t *testing.T) {
	// 2021-03-09 is a Tuesday; 14:05:07.123456; day-of-year 68; ISO week 10.
	ts := time.Date(2021, 3, 9, 14, 5, 7, 123456000, time.UTC)

	cases := []struct{ format, want string }{
		{"YYYY-MM-DD", "2021-03-09"},
		{"YYYY-MM-DD HH24:MI:SS", "2021-03-09 14:05:07"},
		{"HH12:MI AM", "02:05 PM"},
		{"hh12:mi am", "02:05 pm"},
		{"DDD", "068"},
		{"WW", "10"},
		{"IW", "10"},
		{"Q", "1"},
		{"CC", "21"},
		{"D", "3"},  // Tuesday, Sunday=1
		{"ID", "2"}, // ISO Tuesday=2
		{"MS", "123"},
		{"US", "123456"},
		{"Mon", "Mar"},
		{"MON", "MAR"},
		{"Dy", "Tue"},
		{"FMMonth", "March"},
		{"Month", "March    "}, // padded to 9
		{"FMDD", "9"},
		{`"year " YYYY`, "year  2021"},
		{"YY", "21"},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, toChar(ts, c.format), "to_char(ts, %q)", c.format)
	}
}

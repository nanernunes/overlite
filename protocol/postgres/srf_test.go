package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRewriteSelectListSRFs(t *testing.T) {
	cases := map[string]string{
		// One array, at the top level.
		"SELECT unnest(tags) FROM posts": "SELECT _srf0.value FROM posts, json_each(overlite_array_json(tags)) _srf0",
		// Several calls step together; the FROM clause ends at WHERE.
		"SELECT unnest(a) AS x, unnest(b) AS y, generate_subscripts(a, 1) AS i FROM t WHERE k = 1": "SELECT _srf0.value AS x, " +
			"json_extract(overlite_array_json(b), '$[' || _srf0.key || ']') AS y, (_srf0.key + overlite_array_lower(a)) AS i " +
			"FROM t, json_each(overlite_array_json(a)) _srf0 WHERE k = 1",
		// In a subquery and in a CTE, each level on its own.
		"SELECT x FROM (SELECT unnest(a) AS x FROM t ORDER BY 1) s":                      "SELECT x FROM (SELECT _srf0.value AS x FROM t, json_each(overlite_array_json(a)) _srf0 ORDER BY 1) s",
		"WITH r AS (SELECT unnest(k) AS v FROM i -- note\n WHERE NOT d) SELECT v FROM r": "WITH r AS (SELECT _srf0.value AS v FROM i -- note\n, json_each(overlite_array_json(k)) _srf0 WHERE NOT d) SELECT v FROM r",
		// Not in a select list: left to rewriteUnnest.
		"SELECT * FROM unnest(ARRAY[1,2])": "SELECT * FROM unnest(ARRAY[1,2])",
		"SELECT unnest(ARRAY[1,2])":        "SELECT unnest(ARRAY[1,2])",
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteSelectListSRFs(in), "rewriteSelectListSRFs(%q)", in)
	}
}

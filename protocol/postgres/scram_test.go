package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVerifierRoundTrip checks the SCRAM verifier build/parse: deriving from the
// right password reproduces the stored keys, a wrong one does not.
func TestVerifierRoundTrip(t *testing.T) {
	v, err := buildSCRAMVerifier("s3cr3t")
	require.NoError(t, err)

	parsed, ok := parseSCRAMVerifier(v)
	require.True(t, ok)
	assert.True(t, parsed.verifies("s3cr3t"), "correct password verifies")
	assert.False(t, parsed.verifies("wrong"), "wrong password does not")

	_, ok = parseSCRAMVerifier("not-a-verifier")
	assert.False(t, ok)
}

func TestExtractPassword(t *testing.T) {
	pw, ok := extractPassword(`CREATE ROLE alice LOGIN PASSWORD 'alicepw'`)
	assert.True(t, ok)
	assert.Equal(t, "alicepw", pw)

	pw, ok = extractPassword(`ALTER ROLE bob ENCRYPTED PASSWORD 'it''s me'`)
	assert.True(t, ok)
	assert.Equal(t, "it's me", pw)

	_, ok = extractPassword(`ALTER ROLE bob CREATEDB`)
	assert.False(t, ok)
}

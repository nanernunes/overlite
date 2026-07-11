package postgres

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// SCRAM-SHA-256 (RFC 5802 / RFC 7677) SASL authentication. We hold the plaintext
// password (POSTGRES_PASSWORD), so on each login we pick a fresh salt/iteration
// count, derive the SCRAM keys, and verify the client's proof. Channel binding
// (SCRAM-SHA-256-PLUS) is not offered.

const scramIterations = 4096

// scramVerifier holds the SCRAM keys derived from a password (the form Postgres
// stores in rolpassword): enough to authenticate without the plaintext.
type scramVerifier struct {
	iter      int
	salt      []byte
	storedKey []byte
	serverKey []byte
}

// buildSCRAMVerifier derives a fresh verifier for password and encodes it as
// Postgres does: SCRAM-SHA-256$<iter>:<salt>$<StoredKey>:<ServerKey> (base64).
func buildSCRAMVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	v := deriveVerifier(password, salt, scramIterations)
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", v.iter,
		base64.StdEncoding.EncodeToString(v.salt),
		base64.StdEncoding.EncodeToString(v.storedKey),
		base64.StdEncoding.EncodeToString(v.serverKey)), nil
}

// deriveVerifier computes the SCRAM keys for a password with a given salt/iter.
func deriveVerifier(password string, salt []byte, iter int) *scramVerifier {
	salted := pbkdf2SHA256([]byte(password), salt, iter, sha256.Size)
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	return &scramVerifier{
		iter:      iter,
		salt:      salt,
		storedKey: storedKey[:],
		serverKey: hmacSHA256(salted, []byte("Server Key")),
	}
}

// parseSCRAMVerifier decodes a stored "SCRAM-SHA-256$..." verifier string.
func parseSCRAMVerifier(s string) (*scramVerifier, bool) {
	const prefix = "SCRAM-SHA-256$"
	if !strings.HasPrefix(s, prefix) {
		return nil, false
	}
	saltPart, keyPart, ok := strings.Cut(s[len(prefix):], "$")
	if !ok {
		return nil, false
	}
	iterStr, saltB64, ok := strings.Cut(saltPart, ":")
	if !ok {
		return nil, false
	}
	storedB64, serverB64, ok := strings.Cut(keyPart, ":")
	if !ok {
		return nil, false
	}
	iter, err := strconv.Atoi(iterStr)
	if err != nil {
		return nil, false
	}
	salt, e1 := base64.StdEncoding.DecodeString(saltB64)
	stored, e2 := base64.StdEncoding.DecodeString(storedB64)
	server, e3 := base64.StdEncoding.DecodeString(serverB64)
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, false
	}
	return &scramVerifier{iter: iter, salt: salt, storedKey: stored, serverKey: server}, true
}

// verifies reports whether password reproduces this verifier's StoredKey (used
// for cleartext auth against a stored verifier).
func (v *scramVerifier) verifies(password string) bool {
	got := deriveVerifier(password, v.salt, v.iter)
	return hmac.Equal(got.storedKey, v.storedKey)
}

// authSCRAM runs the four-message SASL exchange against cred, returning nil when
// the client proves knowledge of the password. The salt/iteration/keys come from
// a stored verifier when present, otherwise they are derived from the known
// plaintext with a fresh salt.
func (p *Protocol) authSCRAM(c *wireConn, user string, cred credential) error {
	// AuthenticationSASL (10): advertise the one mechanism (list ends with an
	// extra NUL).
	mechs := appendCString(nil, "SCRAM-SHA-256")
	mechs = append(mechs, 0)
	if err := c.send(msgAuthentication, append(i32(10), mechs...)); err != nil {
		return err
	}
	if err := c.flush(); err != nil {
		return err
	}

	// SASLInitialResponse: mechanism, int32 length, client-first-message.
	typ, body, err := c.readMessage()
	if err != nil {
		return err
	}
	if typ != msgPasswordMessage {
		return fmt.Errorf("expected SASL initial response, got %q", string(typ))
	}
	r := newReader(body)
	if r.cstring() != "SCRAM-SHA-256" {
		return fmt.Errorf("unsupported SASL mechanism")
	}
	clientFirst := string(r.bytes(r.int32()))
	if r.err != nil {
		return fmt.Errorf("malformed SASL initial response")
	}
	clientFirstBare := scramBare(clientFirst)
	clientNonce := scramAttr(clientFirstBare, "r")
	if clientNonce == "" {
		return fmt.Errorf("missing client nonce")
	}

	// The verifier: use the role's stored keys, or derive from the plaintext.
	v := cred.scram
	if v == nil {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		v = deriveVerifier(cred.plaintext, salt, scramIterations)
	}

	// AuthenticationSASLContinue (11): server-first-message.
	serverNonce, err := randomNonce()
	if err != nil {
		return err
	}
	nonce := clientNonce + serverNonce
	serverFirst := "r=" + nonce + ",s=" + base64.StdEncoding.EncodeToString(v.salt) +
		",i=" + strconv.Itoa(v.iter)
	if err := c.send(msgAuthentication, append(i32(11), []byte(serverFirst)...)); err != nil {
		return err
	}
	if err := c.flush(); err != nil {
		return err
	}

	// SASLResponse: client-final-message.
	typ, body, err = c.readMessage()
	if err != nil {
		return err
	}
	if typ != msgPasswordMessage {
		return fmt.Errorf("expected SASL response, got %q", string(typ))
	}
	clientFinal := string(body)
	if scramAttr(clientFinal, "r") != nonce {
		_ = p.authFailed(c)
		return fmt.Errorf("scram nonce mismatch")
	}
	proof, err := base64.StdEncoding.DecodeString(scramAttr(clientFinal, "p"))
	if err != nil {
		return fmt.Errorf("bad client proof")
	}

	// Verify the client proof against the StoredKey.
	authMessage := clientFirstBare + "," + serverFirst + "," + scramWithoutProof(clientFinal)
	clientSig := hmacSHA256(v.storedKey, []byte(authMessage))
	if len(proof) != len(clientSig) {
		return p.authFailed(c)
	}
	// ClientKey = ClientProof XOR ClientSignature; it must hash to StoredKey.
	recovered := make([]byte, len(clientSig))
	for i := range recovered {
		recovered[i] = proof[i] ^ clientSig[i]
	}
	got := sha256.Sum256(recovered)
	if !hmac.Equal(got[:], v.storedKey) {
		return p.authFailed(c)
	}

	// AuthenticationSASLFinal (12): send the server signature.
	serverSig := hmacSHA256(v.serverKey, []byte(authMessage))
	final := "v=" + base64.StdEncoding.EncodeToString(serverSig)
	if err := c.send(msgAuthentication, append(i32(12), []byte(final)...)); err != nil {
		return err
	}
	return c.flush()
}

// scramBare strips the GS2 header ("n,," / "y,," / "p=...,,") from the
// client-first-message, leaving the bare "n=user,r=nonce".
func scramBare(clientFirst string) string {
	i1 := strings.IndexByte(clientFirst, ',')
	if i1 < 0 {
		return clientFirst
	}
	i2 := strings.IndexByte(clientFirst[i1+1:], ',')
	if i2 < 0 {
		return clientFirst
	}
	return clientFirst[i1+1+i2+1:]
}

// scramWithoutProof returns the client-final-message with the trailing ",p=..."
// removed (the AuthMessage covers the message up to but not including the proof).
func scramWithoutProof(clientFinal string) string {
	if i := strings.LastIndex(clientFinal, ",p="); i >= 0 {
		return clientFinal[:i]
	}
	return clientFinal
}

// scramAttr extracts the value of the "key=value" attribute from a
// comma-separated SCRAM message.
func scramAttr(msg, key string) string {
	for _, part := range strings.Split(msg, ",") {
		if strings.HasPrefix(part, key+"=") {
			return part[len(key)+1:]
		}
	}
	return ""
}

// randomNonce returns a base64 printable nonce (SCRAM nonces must avoid commas).
func randomNonce() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// pbkdf2SHA256 is PBKDF2 with HMAC-SHA-256 (RFC 2898), enough for SCRAM's key
// derivation without pulling in golang.org/x/crypto.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	blocks := (keyLen + hLen - 1) / hLen
	var dk []byte
	var idx [4]byte
	for block := 1; block <= blocks; block++ {
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		u := hmacSHA256(password, append(append([]byte{}, salt...), idx[:]...))
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			u = hmacSHA256(password, u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

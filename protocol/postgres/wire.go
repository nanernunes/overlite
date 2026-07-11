package postgres

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// Frontend/Backend message type bytes we care about. See the PostgreSQL
// message-format docs; startup messages carry no type byte.
const (
	// Frontend messages.
	msgQuery           = 'Q' // simple query
	msgParse           = 'P' // extended: parse a statement
	msgBind            = 'B' // extended: bind a portal
	msgDescribe        = 'D' // extended: describe statement/portal
	msgExecute         = 'E' // extended: execute a portal
	msgSync            = 'S' // extended: end of a request cycle
	msgFlush           = 'H' // extended: flush pending output
	msgClose           = 'C' // extended: close statement/portal
	msgPasswordMessage = 'p' // password response
	msgTerminate       = 'X' // frontend closing

	// Backend messages.
	msgAuthentication       = 'R'
	msgParameterStatus      = 'S'
	msgBackendKeyData       = 'K'
	msgReadyForQuery        = 'Z'
	msgRowDescription       = 'T'
	msgDataRow              = 'D'
	msgCommandComplete      = 'C'
	msgEmptyQuery           = 'I'
	msgErrorResponse        = 'E'
	msgParseComplete        = '1'
	msgBindComplete         = '2'
	msgCloseComplete        = '3'
	msgParameterDescription = 't'
	msgNoData               = 'n'
	msgCopyInResponse       = 'G'
	msgCopyOutResponse      = 'H'
	msgCopyData             = 'd' // both directions
	msgCopyDone             = 'c' // both directions
	msgCopyFail             = 'f' // frontend
	msgPortalSuspended      = 's' // Execute stopped at max-rows; more rows remain
)

// Magic protocol codes carried in the first int32 after the startup length.
const (
	protocolVersion3  = 196608   // 3.0
	cancelRequestCode = 80877102 // client asking to cancel a running query
	sslRequestCode    = 80877103 // client asking to start TLS
	gssRequestCode    = 80877104 // client asking to start GSSAPI
)

var byteOrder = binary.BigEndian

// wireConn wraps a raw connection with buffered framed I/O for the Postgres
// protocol.
type wireConn struct {
	raw     net.Conn
	r       *bufio.Reader
	w       *bufio.Writer
	secured bool // the connection was upgraded to TLS
}

func newWireConn(c net.Conn) *wireConn {
	return &wireConn{raw: c, r: bufio.NewReader(c), w: bufio.NewWriter(c)}
}

// readStartup handles SSL/GSS negotiation and returns the key/value parameters
// from the StartupMessage. If tlsConfig is non-nil, an SSLRequest is accepted
// ('S') and the connection is upgraded to TLS; otherwise it is declined ('N')
// and the client falls back to plaintext.
func (c *wireConn) readStartup(tlsConfig *tls.Config) (map[string]string, *cancelRequest, error) {
	for {
		length, err := c.readInt32()
		if err != nil {
			return nil, nil, err
		}
		if length < 8 {
			return nil, nil, fmt.Errorf("startup: bogus length %d", length)
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(c.r, body); err != nil {
			return nil, nil, err
		}
		code := int32(byteOrder.Uint32(body[:4]))

		switch code {
		case sslRequestCode:
			if tlsConfig == nil {
				if err := c.writeRaw([]byte{'N'}); err != nil {
					return nil, nil, err
				}
				continue
			}
			if err := c.writeRaw([]byte{'S'}); err != nil {
				return nil, nil, err
			}
			if err := c.upgradeTLS(tlsConfig); err != nil {
				return nil, nil, err
			}
			continue
		case gssRequestCode:
			// We don't support GSSAPI encryption; decline.
			if err := c.writeRaw([]byte{'N'}); err != nil {
				return nil, nil, err
			}
			continue
		case cancelRequestCode:
			if len(body) < 12 {
				return nil, nil, fmt.Errorf("cancel request: short body")
			}
			return nil, &cancelRequest{
				pid:    int32(byteOrder.Uint32(body[4:8])),
				secret: int32(byteOrder.Uint32(body[8:12])),
			}, nil
		case protocolVersion3:
			return parseStartupParams(body[4:]), nil, nil
		default:
			return nil, nil, fmt.Errorf("unsupported protocol/startup code %d", code)
		}
	}
}

// upgradeTLS performs the server-side TLS handshake and switches the buffered
// reader/writer to the encrypted connection. The client sends only the
// SSLRequest before waiting for our reply, so no buffered plaintext is lost.
func (c *wireConn) upgradeTLS(cfg *tls.Config) error {
	tconn := tls.Server(c.raw, cfg)
	if err := tconn.Handshake(); err != nil {
		return fmt.Errorf("tls handshake: %w", err)
	}
	c.raw = tconn
	c.r = bufio.NewReader(tconn)
	c.w = bufio.NewWriter(tconn)
	c.secured = true
	return nil
}

func parseStartupParams(b []byte) map[string]string {
	params := map[string]string{}
	parts := splitCStrings(b)
	for i := 0; i+1 < len(parts); i += 2 {
		params[parts[i]] = parts[i+1]
	}
	return params
}

// readMessage reads one typed frontend message, returning its type byte and
// body (without the length prefix).
func (c *wireConn) readMessage() (byte, []byte, error) {
	typ, err := c.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length, err := c.readInt32()
	if err != nil {
		return 0, nil, err
	}
	if length < 4 {
		return 0, nil, fmt.Errorf("message %c: bogus length %d", typ, length)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return 0, nil, err
	}
	return typ, body, nil
}

func (c *wireConn) readInt32() (int32, error) {
	var v int32
	if err := binary.Read(c.r, byteOrder, &v); err != nil {
		return 0, err
	}
	return v, nil
}

// send writes a typed backend message from an already-built body.
func (c *wireConn) send(typ byte, body []byte) error {
	if err := c.w.WriteByte(typ); err != nil {
		return err
	}
	if err := binary.Write(c.w, byteOrder, int32(len(body)+4)); err != nil {
		return err
	}
	_, err := c.w.Write(body)
	return err
}

func (c *wireConn) writeRaw(b []byte) error {
	if _, err := c.w.Write(b); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *wireConn) flush() error { return c.w.Flush() }

// splitCStrings splits a buffer of NUL-terminated strings, dropping the final
// empty terminator.
func splitCStrings(b []byte) []string {
	var out []string
	start := 0
	for i, ch := range b {
		if ch == 0 {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	return out
}

package logging

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"time"
)

// syslogFacilities maps the supported facility names to RFC 5424 codes.
var syslogFacilities = map[string]int{
	"user": 1, "daemon": 3, "auth": 4, "authpriv": 10,
	"local0": 16, "local1": 17, "local2": 18, "local3": 19, "local4": 20, "local5": 21, "local6": 22, "local7": 23,
}

func syslogFacility(name string) (int, bool) {
	if name == "" {
		return syslogFacilities["local0"], true
	}
	code, ok := syslogFacilities[name]
	return code, ok
}

// maxSyslogDatagram keeps a datagram below the UDP payload limit.
const maxSyslogDatagram = 32 << 10

// syslogSender sends RFC 5424 messages. It is written by hand rather than on
// log/syslog, which does not build for Windows.
type syslogSender struct {
	network, address  string
	facility          int
	app, host, procID string
	octetCounting     bool
	timeout           time.Duration
	conn              net.Conn
	// dial is replaced in tests.
	dial func(ctx context.Context) (net.Conn, error)
}

func newSyslogSender(o SyslogOptions, timeout time.Duration) *syslogSender {
	s := &syslogSender{network: o.network(), address: o.Address, timeout: timeout, app: o.AppName, host: o.Hostname,
		octetCounting: o.Framing != "newline", procID: strconv.Itoa(os.Getpid())}
	s.facility, _ = syslogFacility(o.Facility)
	if s.app == "" {
		s.app = "jelee"
	}
	if s.host == "" {
		s.host = sanitizeSyslogField(hostnameOrNil(), 255)
	}
	s.dial = s.defaultDial
	return s
}

func hostnameOrNil() string {
	if name, err := os.Hostname(); err == nil {
		return name
	}
	return "-"
}

func (s *syslogSender) datagram() bool { return s.network == "udp" || s.network == "unixgram" }

func (s *syslogSender) maxRecord() int {
	if s.datagram() {
		return maxSyslogDatagram
	}
	return 0
}

func (s *syslogSender) defaultDial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: s.timeout}
	if s.network == "tls" {
		td := tls.Dialer{NetDialer: &d, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
		return td.DialContext(ctx, "tcp", s.address)
	}
	return d.DialContext(ctx, s.network, s.address)
}

func (s *syslogSender) close() {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

func (s *syslogSender) send(ctx context.Context, records [][]byte) error {
	if s.conn == nil {
		dctx, cancel := context.WithTimeout(ctx, s.timeout)
		conn, err := s.dial(dctx)
		cancel()
		if err != nil {
			return classifyNetError(err)
		}
		s.conn = conn
	}
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetWriteDeadline(deadline)
	var err error
	if s.datagram() {
		for _, rec := range records {
			if _, err = s.conn.Write(s.message(rec, false)); err != nil {
				break
			}
		}
	} else {
		var buf []byte
		for _, rec := range records {
			buf = append(buf, s.message(rec, true)...)
		}
		_, err = s.conn.Write(buf)
	}
	if err != nil {
		s.close()
		return classifyNetError(err)
	}
	return nil
}

// recordMeta is the part of a JSON record the destinations need.
type recordMeta struct {
	Time      string `json:"time"`
	Level     string `json:"level"`
	Component string `json:"component"`
}

func parseRecordMeta(rec []byte) (meta recordMeta, at time.Time) {
	if json.Unmarshal(rec, &meta) != nil {
		meta = recordMeta{}
	}
	at, err := time.Parse(time.RFC3339Nano, meta.Time)
	if err != nil {
		at = time.Now()
	}
	return meta, at
}

// syslogSeverity maps a slog level name to an RFC 5424 severity.
func syslogSeverity(level string) int {
	switch level {
	case "DEBUG":
		return 7
	case "WARN":
		return 4
	case "ERROR":
		return 3
	default:
		return 6
	}
}

// message renders one record as an RFC 5424 message; the JSON record is the
// MSG part, so every field survives. framed adds the stream framing.
func (s *syslogSender) message(rec []byte, framed bool) []byte {
	meta, at := parseRecordMeta(rec)
	msgID := sanitizeSyslogField(meta.Component, 32)
	body := make([]byte, 0, len(rec)+160)
	body = append(body, '<')
	body = strconv.AppendInt(body, int64(s.facility*8+syslogSeverity(meta.Level)), 10)
	body = append(body, ">1 "...)
	body = at.UTC().AppendFormat(body, "2006-01-02T15:04:05.000000Z")
	body = append(body, ' ')
	body = append(body, s.host...)
	body = append(body, ' ')
	body = append(body, s.app...)
	body = append(body, ' ')
	body = append(body, s.procID...)
	body = append(body, ' ')
	body = append(body, msgID...)
	body = append(body, " - \xEF\xBB\xBF"...)
	body = append(body, rec...)
	if !framed {
		return body
	}
	if !s.octetCounting {
		return append(body, '\n')
	}
	out := strconv.AppendInt(make([]byte, 0, len(body)+8), int64(len(body)), 10)
	out = append(out, ' ')
	return append(out, body...)
}

// sanitizeSyslogField makes a header field PRINTUSASCII of at most max
// bytes, or the nil value.
func sanitizeSyslogField(s string, limit int) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(out) < limit; i++ {
		if s[i] >= 33 && s[i] <= 126 {
			out = append(out, s[i])
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return string(out)
}

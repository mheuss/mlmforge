package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ConnectTimeoutError reports a connect attempt the driver ended by timeout.
type ConnectTimeoutError struct {
	Hosts  string
	Waited time.Duration
	Err    error
}

func (e *ConnectTimeoutError) Error() string {
	return fmt.Sprintf("no connection to %s was made; waited %.1fs", e.Hosts, e.Waited.Seconds())
}

func (e *ConnectTimeoutError) Unwrap() error { return e.Err }

// TimeConnect runs connect and returns a ConnectTimeoutError when the driver reports a timeout.
func TimeConnect(ctx context.Context, dbURL string, connect func() error) error {
	start := time.Now()
	err := connect()
	waited := time.Since(start)
	if err == nil || ctx.Err() != nil || !isTimeout(err) {
		return err
	}
	hosts, ok := connectHosts(dbURL)
	if !ok {
		return err
	}
	return &ConnectTimeoutError{Hosts: hosts, Waited: waited, Err: err}
}

// isTimeout reports whether err carries a driver timeout.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// connectHosts states the host and port fields a postgres URL writes, copied raw.
func connectHosts(dbURL string) (string, bool) {
	rest, ok := strings.CutPrefix(dbURL, "postgres://")
	if !ok {
		if rest, ok = strings.CutPrefix(dbURL, "postgresql://"); !ok {
			return "", false
		}
	}
	if _, err := url.Parse(dbURL); err != nil {
		return "", false
	}
	// Order matters: a '?' after '#' is not a query, and a '/' after '?' is not a path.
	beforeFragment, _, _ := strings.Cut(rest, "#")
	beforeQuery, rawQuery, _ := strings.Cut(beforeFragment, "?")
	authority, _, _ := strings.Cut(beforeQuery, "/")
	// An '@' past the authority means a raw '/', '?' or '#' split the userinfo, so the host part could hold password text.
	if strings.Count(rest, "@") != strings.Count(authority, "@") {
		return "", false
	}
	// Without this, a malformed pair such as 'port=1;password=x' would be echoed whole below.
	if _, err := url.ParseQuery(rawQuery); err != nil {
		return "", false
	}
	hostPart := authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		hostPart = authority[at+1:]
	}

	hasPort := hostPartHasPort(hostPart)
	hasQueryHost := false
	var labels []string
	for _, pair := range strings.Split(rawQuery, "&") {
		rawKey, _, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			continue
		}
		switch key {
		case "host":
			hasQueryHost = true
			labels = append(labels, "query "+pair)
		case "port":
			hasPort = true
			labels = append(labels, "query "+pair)
		}
	}

	var b strings.Builder
	switch {
	case hostPart != "":
		b.WriteString(hostPart)
	case !hasQueryHost:
		b.WriteString("the URL names no host")
	}
	if len(labels) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("(" + strings.Join(labels, ", ") + ")")
	}
	if !hasPort {
		b.WriteString(" (the URL sets no port)")
	}
	return b.String(), true
}

// hostPartHasPort reports whether any comma-separated entry names a port.
func hostPartHasPort(hostPart string) bool {
	for _, entry := range strings.Split(hostPart, ",") {
		if _, port, err := net.SplitHostPort(entry); err == nil && port != "" {
			return true
		}
	}
	return false
}

package platform

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeTimeout is a net.Error that reports a timeout.
type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "read tcp: i/o timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return false }

// fakeRefused is a net.Error that does not report a timeout.
type fakeRefused struct{}

func (fakeRefused) Error() string   { return "dial tcp: connect: connection refused" }
func (fakeRefused) Timeout() bool   { return false }
func (fakeRefused) Temporary() bool { return false }

func TestConnectHosts_StatesTheURLsHostFields(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "host and port", url: "postgres://u:s3cret@db:5432/app", want: "db:5432"},
		{name: "postgresql scheme, no port", url: "postgresql://db/app", want: "db (the URL sets no port)"},
		{name: "several hosts", url: "postgres://u:s3cret@h1:5432,h2:5433/app", want: "h1:5432,h2:5433"},
		{name: "no host", url: "postgres:///app", want: "the URL names no host (the URL sets no port)"},
		{name: "query host and port", url: "postgres:///app?host=%2Ftmp&port=5433", want: "(query host=%2Ftmp, query port=5433)"},
		{name: "query port only", url: "postgres://u@db/app?port=5433&password=s3cret", want: "db (query port=5433)"},
		{name: "encoded at-sign in the password", url: "postgres://u:p%40ss@db:5432/app", want: "db:5432"},
		{name: "query and fragment", url: "postgres://db:5432?sslmode=disable#frag", want: "db:5432"},
		{name: "IPv6 host", url: "postgres://u@[::1]:5432/app", want: "[::1]:5432"},
		{name: "raw at-sign in the password", url: "postgres://u:p@s3cret@db:5432/app", want: "db:5432"},
		{name: "slash after a fragment", url: "postgres://db:5432#a/b", want: "db:5432"},
		{name: "query after a fragment", url: "postgres://db:5432/app#x?host=evil", want: "db:5432"},
		{name: "slash inside the query", url: "postgres://db:5432?sslmode=disable&x=/y", want: "db:5432"},
		{name: "encoded query keys", url: "postgres://db/app?h%6fst=other&p%6frt=5433", want: "db (query h%6fst=other, query p%6frt=5433)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := connectHosts(tt.url)

			require.True(t, ok)
			require.Equal(t, tt.want, got)
			require.NotContains(t, got, "s3cret")
		})
	}
}

func TestConnectHosts_RefusesAStringItCannotRead(t *testing.T) {
	for _, raw := range []string{
		"host=db dbname=app",
		"mysql://db/app",
		"postgres://u:p@[::1]:5432,[::2]/app",
		"postgres://u@db/app?port=5433;password=s3cret",
		"postgres://u@db/app?host=db2;password=s3cret&connect_timeout=1",
		"postgres://u@db/app?host=%zz",
		"postgres://u:12/s3cret@db/app",
		"postgres://u:12?s3cret@db/app",
		"postgres://u:12#s3cret@db/app",
		"postgres://db/app?host=x password=s3cret",
		"postgres://db/app?host=x+password=s3cret",
		"postgres://db/app?host=x%20password%3Ds3cret",
		"postgres://db/app?port=5433%20password%3Ds3cret",
		"postgres://u:p@x/s3cret@db/app",
	} {
		_, ok := connectHosts(raw)

		require.False(t, ok, raw)
	}
}

func TestTimeConnect_WrapsANetTimeout(t *testing.T) {
	driverErr := fmt.Errorf("dial: %w", fakeTimeout{})

	err := TimeConnect(context.Background(), "postgres://u:s3cret@db:5432/app", func() error {
		time.Sleep(20 * time.Millisecond)
		return driverErr
	})

	var cte *ConnectTimeoutError
	require.ErrorAs(t, err, &cte)
	require.Equal(t, "db:5432", cte.Hosts)
	require.GreaterOrEqual(t, cte.Waited, 20*time.Millisecond)
	require.ErrorIs(t, err, driverErr)
	require.Regexp(t, `^no connection to db:5432 was made; waited \d+\.\ds$`, err.Error())
}

func TestTimeConnect_WrapsADeadlineExceeded(t *testing.T) {
	driverErr := fmt.Errorf("failed to connect: %w", context.DeadlineExceeded)

	err := TimeConnect(context.Background(), "postgres://db:5432/app", func() error { return driverErr })

	var cte *ConnectTimeoutError
	require.ErrorAs(t, err, &cte)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestTimeConnect_PassesThroughWhenTheCallerWasCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	driverErr := fmt.Errorf("dial: %w", fakeTimeout{})

	err := TimeConnect(ctx, "postgres://db:5432/app", func() error { return driverErr })

	require.Same(t, driverErr, err)
}

func TestTimeConnect_PassesThroughANonTimeout(t *testing.T) {
	refused := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

	err := TimeConnect(context.Background(), "postgres://db:5432/app", func() error { return refused })

	require.Same(t, refused, err)
}

func TestTimeConnect_PassesThroughANetErrorThatIsNotATimeout(t *testing.T) {
	refused := fmt.Errorf("dial: %w", fakeRefused{})

	err := TimeConnect(context.Background(), "postgres://db:5432/app", func() error { return refused })

	require.Same(t, refused, err)
}

func TestTimeConnect_PassesThroughAStringItCannotRead(t *testing.T) {
	for _, raw := range []string{"host=db dbname=app", "postgres://u:p@[::1]:5432,[::2]/app"} {
		driverErr := fmt.Errorf("dial: %w", fakeTimeout{})

		err := TimeConnect(context.Background(), raw, func() error { return driverErr })

		require.Same(t, driverErr, err, raw)
	}
}

func TestTimeConnect_ReturnsNilOnSuccess(t *testing.T) {
	require.NoError(t, TimeConnect(context.Background(), "postgres://db:5432/app", func() error { return nil }))
}

func TestConnectTimeoutError_TextHoldsOnlyTheHostsAndTheWait(t *testing.T) {
	err := &ConnectTimeoutError{
		Hosts:  "db:5432",
		Waited: 1500 * time.Millisecond,
		Err:    errors.New("failed to connect to `user=u database=app` with password s3cret"),
	}

	require.Equal(t, "no connection to db:5432 was made; waited 1.5s", err.Error())
}

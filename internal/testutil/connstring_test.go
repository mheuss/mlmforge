package testutil

import (
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestPasswordWindows_FindsAPartialLeak(t *testing.T) {
	found := PasswordWindows(`invalid port ":Zm9v" after host`, "Zm9vQmFy")

	require.Equal(t, []string{"Zm9v"}, found)
}

func TestPasswordWindows_FindsNothingInUnrelatedText(t *testing.T) {
	require.Empty(t, PasswordWindows("open database pool: pgx refused the connection string.", "Zm9vQmFy/cXV4eHl6"))
}

func TestConnStringCases_EachStringCarriesItsPassword(t *testing.T) {
	for _, tc := range ConnStringCases() {
		require.True(t, strings.Contains(tc.ConnString, tc.Password), tc.Name)
	}
}

func TestConnStringCases_NoPasswordOverlapsTheTextAroundIt(t *testing.T) {
	for _, tc := range ConnStringCases() {
		known := []string{
			tc.WithoutPassword(),
			RefusalText(t, "golang-migrate", "parse"),
			RefusalText(t, "golang-migrate", "scheme"),
			RefusalText(t, "golang-migrate", "refused"),
			RefusalText(t, "pgx", "parse"),
			RefusalText(t, "pgx", "refused"),
			PreDriverText(t, "raw-at", "path"),
			PreDriverText(t, "raw-at", "query"),
			PreDriverText(t, "raw-at", "fragment"),
			PreDriverText(t, "scheme-case", ""),
		}
		for _, k := range known {
			require.Empty(t, PasswordWindows(k, tc.Password), "%s overlaps %q", tc.Name, k)
		}
	}
}

func TestIsolatePgxEnv_PointsTheServiceFilesAtAnEmptyDirectory(t *testing.T) {
	IsolatePgxEnv(t)

	require.True(t, strings.HasSuffix(os.Getenv("PGSERVICEFILE"), "pg_service.conf"))
	_, err := os.Stat(os.Getenv("PGSERVICEFILE"))
	require.True(t, os.IsNotExist(err))
	require.NotEmpty(t, os.Getenv("PGSYSCONFDIR"))
}

func TestResolveRefusedCases_EachStringCarriesItsPassword(t *testing.T) {
	for _, tc := range ResolveRefusedCases() {
		require.True(t, strings.Contains(tc.ConnString, tc.Password), tc.Name)
		require.True(t, strings.Contains(tc.Wiring, tc.WiringPassword), tc.Name)
		require.Equal(t, tc.Password == "", tc.WiringPassword == "", tc.Name)
		require.NotEmpty(t, tc.Twin, tc.Name)
		require.NotEqual(t, tc.ConnString, tc.Twin, tc.Name)
		require.Contains(t, []string{TwinParsers, TwinPgxRefuses, TwinResolve}, tc.TwinCheck, tc.Name)
		require.NotEmpty(t, PreDriverText(t, tc.Stage, tc.Part), tc.Name)
	}
}

func TestResolveRefusedCases_NoPasswordOverlapsTheTextAroundIt(t *testing.T) {
	for _, tc := range ResolveRefusedCases() {
		if tc.Password == "" {
			continue
		}
		want := PreDriverText(t, tc.Stage, tc.Part)
		require.Empty(t, PasswordWindows(want, tc.Password), tc.Name)
		require.Empty(t, PasswordWindows(strings.ReplaceAll(tc.ConnString, tc.Password, ""), tc.Password), tc.Name)
		require.Empty(t, PasswordWindows(want, tc.WiringPassword), tc.Name)
		require.Empty(t, PasswordWindows(tc.WiringWithoutPassword(), tc.WiringPassword), tc.Name)
	}
}

func TestReachesDriverCases_EachRowNamesADial(t *testing.T) {
	for _, tc := range ReachesDriverCases() {
		require.True(t, strings.HasPrefix(tc.Dial, "dial tcp 127.0.0.1:"), tc.Name)
		u, err := url.Parse(tc.ConnString)
		require.NoError(t, err, tc.Name)
		require.Equal(t, "127.0.0.1", u.Hostname(), tc.Name)
	}
}

func TestResolveRefusedCases_EachWiringHostCannotReachDNS(t *testing.T) {
	ClearTimeoutEnv(t)
	for _, name := range []string{"PGHOST", "PGPORT"} {
		t.Setenv(name, "")
	}
	for _, tc := range ResolveRefusedCases() {
		if !strings.HasPrefix(tc.Wiring, "postgres://") && !strings.HasPrefix(tc.Wiring, "postgresql://") {
			requirePgxDialsNoName(t, tc)
			continue
		}
		u, err := url.Parse(tc.Wiring)
		require.NoError(t, err, tc.Name)
		hosts := []string{u.Hostname()}
		if queryHost := u.Query().Get("host"); queryHost != "" {
			hosts = append(hosts, queryHost)
		}
		for _, host := range hosts {
			require.True(t, strings.HasSuffix(host, ".invalid") || net.ParseIP(host) != nil,
				"%s: wiring host %q is neither a .invalid name nor a literal IP", tc.Name, host)
		}
	}
}

// requirePgxDialsNoName fails the test when pgx accepts a wiring string and would dial any host that is not a socket path.
func requirePgxDialsNoName(t *testing.T, tc ResolveRefusedCase) {
	t.Helper()
	cfg, err := pgconn.ParseConfig(tc.Wiring)
	if err != nil {
		return
	}
	hosts := []string{cfg.Host}
	for _, fallback := range cfg.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	for _, host := range hosts {
		require.True(t, strings.HasPrefix(host, "/"), "%s: pgx parsed the wiring string with host %q, which is not a socket path", tc.Name, host)
	}
}

func TestResolveRefusedCases_EachDialableTwinIsAMustReachRow(t *testing.T) {
	reaches := map[string]bool{}
	for _, tc := range ReachesDriverCases() {
		reaches[tc.ConnString] = true
	}
	checked := 0
	for _, tc := range ResolveRefusedCases() {
		u, err := url.Parse(tc.Twin)
		require.NoError(t, err, tc.Name)
		if u.Hostname() != "127.0.0.1" {
			continue
		}
		checked++
		require.True(t, reaches[tc.Twin], "%s: twin %q is not a ReachesDriverCases row", tc.Name, tc.Twin)
	}
	require.Equal(t, 2, checked, "twins that dial 127.0.0.1")
}

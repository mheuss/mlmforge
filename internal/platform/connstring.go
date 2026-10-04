package platform

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

type connDriver string

const (
	driverMigrate  connDriver = "golang-migrate"
	driverPgx      connDriver = "pgx"
	driverMlmforge connDriver = "mlmforge"
)

type connStage string

const (
	stageParse         connStage = "parse"
	stageScheme        connStage = "scheme"
	stageRefused       connStage = "refused"
	stageRawAt         connStage = "raw-at"
	stageSchemeCase    connStage = "scheme-case"
	stageKeywordKey    connStage = "keyword-key"
	stageAfterPassword connStage = "after-password"
	stageRawPlus       connStage = "raw-plus"
)

type connPart string

const (
	partPath     connPart = "path"
	partQuery    connPart = "query"
	partFragment connPart = "fragment"
)

// ConnStringError reports a connection string that mlmforge or a driver refused, holding nothing from the string.
type ConnStringError struct {
	driver connDriver
	stage  connStage
	part   connPart
}

// Driver names the driver whose call site refused the connection string, or mlmforge for a check that runs before any driver.
func (e *ConnStringError) Driver() string { return string(e.driver) }

// Stage names the check or parse step that refused the connection string.
func (e *ConnStringError) Stage() string { return string(e.stage) }

// Part names where a raw-at refusal found the '@', and is empty for every other stage.
func (e *ConnStringError) Part() string { return string(e.part) }

func (e *ConnStringError) Error() string {
	switch e.stage {
	case stageScheme:
		return "mlmforge migrate accepts only a connection string that starts with postgres:// or postgresql://. The connection string is withheld because it can contain a password."
	case stageRawAt:
		// Concatenated, not formatted: the text holds %40.
		return "mlmforge found a raw @ after the host part, in the connection string's " + string(e.part) +
			". If a password holds @ / ? or #, percent-encode them. Write any other literal @ there as %40." +
			" The connection string is withheld because it can contain a password."
	case stageSchemeCase:
		return "mlmforge found a connection string whose scheme is not all lowercase. Write postgres:// or postgresql:// in lowercase." +
			" The connection string is withheld because it can contain a password."
	case stageKeywordKey:
		return "mlmforge found a connection string that is not a postgres:// or postgresql:// URL, and its first keyword holds a character no setting name can hold." +
			" Check for a stray character, such as a space or a quote, before postgres://." +
			" The connection string is withheld because it can contain a password."
	case stageAfterPassword:
		// Concatenated, not formatted: the text holds %26.
		return "mlmforge found a query key or & after password in the connection string." +
			" Put password last in the query, or move the password into the user part before the @." +
			" Write each & in the password as %26." +
			" The connection string is withheld because it can contain a password."
	case stageRawPlus:
		// Concatenated, not formatted: the text holds %2B and %20.
		return "mlmforge found a raw + in the connection string's query password." +
			" A raw + in a query value is read as a space." +
			" Write a plus as %2B and a space as %20." +
			" The connection string is withheld because it can contain a password."
	}
	what := "could not parse the connection string"
	if e.stage == stageRefused {
		what = "refused the connection string"
	}
	return fmt.Sprintf("%s %s. The connection string and %s's message are withheld because they can contain a password.",
		e.driver, what, e.driver)
}

// PgxConnStringError returns a ConnStringError in place of a pgx parse error, and any other error unchanged.
func PgxConnStringError(err error) error {
	var pce *pgconn.ParseConfigError
	if !errors.As(err, &pce) {
		return err
	}
	return &ConnStringError{driver: driverPgx, stage: pgxStage(pce.ConnString)}
}

// pgxStage reports parse for a URL-form string that url.Parse rejects, and refused otherwise.
func pgxStage(connString string) connStage {
	if !strings.HasPrefix(connString, "postgres://") && !strings.HasPrefix(connString, "postgresql://") {
		return stageRefused
	}
	if _, err := url.Parse(connString); err != nil {
		return stageParse
	}
	return stageRefused
}

// migrateConnStringError returns a ConnStringError in place of a URL parse error, and any other error unchanged.
func migrateConnStringError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) || ue.Op != "parse" {
		return err
	}
	return &ConnStringError{driver: driverMigrate, stage: stageParse}
}

// migrateSchemeError returns a ConnStringError for a string without a postgres:// or postgresql:// prefix, and nil otherwise.
func migrateSchemeError(dbURL string) error {
	if strings.HasPrefix(dbURL, "postgres://") || strings.HasPrefix(dbURL, "postgresql://") {
		return nil
	}
	return &ConnStringError{driver: driverMigrate, stage: stageScheme}
}

// PreDriverError returns a ConnStringError for a connection string mlmforge refuses before any driver sees it, and nil otherwise.
func PreDriverError(dbURL string) error {
	// Order matters: where two checks refuse one string, the earlier check's stage is reported.
	for _, check := range []func(string) error{schemeCaseError, keywordKeyError, rawAtError, afterPasswordError, rawPlusError} {
		if err := check(dbURL); err != nil {
			return err
		}
	}
	return nil
}

// schemeCaseError returns a ConnStringError for a string whose postgres:// or postgresql:// scheme is not written in lowercase, and nil otherwise.
func schemeCaseError(dbURL string) error {
	for _, scheme := range []string{"postgres://", "postgresql://"} {
		if len(dbURL) >= len(scheme) && strings.EqualFold(dbURL[:len(scheme)], scheme) && !strings.HasPrefix(dbURL, scheme) {
			return &ConnStringError{driver: driverMlmforge, stage: stageSchemeCase}
		}
	}
	return nil
}

// keywordKeyError returns a ConnStringError for a non-URL string whose first keyword key holds an ASCII byte outside A-Z a-z 0-9 _ . $, unless it starts with _pq_., and nil otherwise.
func keywordKeyError(dbURL string) error {
	if strings.HasPrefix(dbURL, "postgres://") || strings.HasPrefix(dbURL, "postgresql://") {
		return nil
	}
	before, _, found := strings.Cut(dbURL, "=")
	if !found {
		return nil
	}
	key := strings.Trim(before, " \t\n\r\v\f")
	// Removing the _pq_. exemption refuses a string that connects today.
	if key == "" || strings.HasPrefix(key, "_pq_.") {
		return nil
	}
	for i := 0; i < len(key); i++ {
		if isRefusedKeyByte(key[i]) {
			return &ConnStringError{driver: driverMlmforge, stage: stageKeywordKey}
		}
	}
	return nil
}

// isRefusedKeyByte reports whether b is an ASCII byte outside A-Z, a-z, 0-9, '_', '.' and '$'.
func isRefusedKeyByte(b byte) bool {
	switch {
	case b >= 0x80:
		return false
	case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9':
		return false
	case b == '_', b == '.', b == '$':
		return false
	}
	return true
}

// querySegment is one &-separated piece of a URL's query.
type querySegment struct {
	key   string
	value string
}

// querySegments returns every &-separated segment of a postgres:// or postgresql:// string's query, empty ones included, and false for any other string.
func querySegments(dbURL string) ([]querySegment, bool) {
	if !strings.HasPrefix(dbURL, "postgres://") && !strings.HasPrefix(dbURL, "postgresql://") {
		return nil, false
	}
	beforeFragment, _, _ := strings.Cut(dbURL, "#")
	_, query, found := strings.Cut(beforeFragment, "?")
	if !found {
		return nil, true
	}
	var segments []querySegment
	for _, raw := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(raw, "=")
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		segments = append(segments, querySegment{key: key, value: value})
	}
	return segments, true
}

// isPasswordKey reports whether a decoded query key reads "password" in any letter case once surrounding whitespace is trimmed.
func isPasswordKey(key string) bool {
	return strings.EqualFold(strings.TrimFunc(key, unicode.IsSpace), "password")
}

// afterPasswordError returns a ConnStringError when any '&' follows the start of the query's first password segment, and nil otherwise.
func afterPasswordError(dbURL string) error {
	segments, ok := querySegments(dbURL)
	if !ok {
		return nil
	}
	for i, s := range segments {
		if isPasswordKey(s.key) {
			if i < len(segments)-1 {
				return &ConnStringError{driver: driverMlmforge, stage: stageAfterPassword}
			}
			return nil
		}
	}
	return nil
}

// rawPlusError returns a ConnStringError when the query's first password value holds a raw '+', and nil otherwise.
func rawPlusError(dbURL string) error {
	segments, ok := querySegments(dbURL)
	if !ok {
		return nil
	}
	for _, s := range segments {
		if isPasswordKey(s.key) {
			if strings.Contains(s.value, "+") {
				return &ConnStringError{driver: driverMlmforge, stage: stageRawPlus}
			}
			return nil
		}
	}
	return nil
}

// rawAtError returns a ConnStringError for a postgres:// or postgresql:// string with a raw '@' after the host part, and nil otherwise.
func rawAtError(dbURL string) error {
	rest, ok := strings.CutPrefix(dbURL, "postgres://")
	if !ok {
		if rest, ok = strings.CutPrefix(dbURL, "postgresql://"); !ok {
			return nil
		}
	}
	// Cut in this order. Any other order puts an '@' in the wrong part, or misses one.
	beforeFragment, fragment, _ := strings.Cut(rest, "#")
	beforeQuery, query, _ := strings.Cut(beforeFragment, "?")
	_, path, _ := strings.Cut(beforeQuery, "/")
	for _, p := range []struct {
		part connPart
		text string
	}{{partPath, path}, {partQuery, query}, {partFragment, fragment}} {
		if strings.Contains(p.text, "@") {
			return &ConnStringError{driver: driverMlmforge, stage: stageRawAt, part: p.part}
		}
	}
	return nil
}

// libPQEnvProbe is a connection string that holds nothing from any operator's string.
const libPQEnvProbe = "postgres://u@h/d"

// migrateDriverParseError returns lib/pq's own error for a refused environment or an undetectable user, a ConnStringError when lib/pq refuses the filtered connection string, and nil otherwise.
func migrateDriverParseError(dbURL string) error {
	purl, err := url.Parse(dbURL)
	if err != nil {
		return nil
	}
	_, err = pq.NewConnector(migrate.FilterCustomQuery(purl).String())
	if err == nil {
		return nil
	}
	// Order matters: probed only after the string fails, so a string that overrides a bad environment still passes.
	if _, probeErr := pq.NewConnector(libPQEnvProbe); probeErr != nil {
		return probeErr
	}
	if errors.Is(err, pq.ErrCouldNotDetectUsername) {
		return err
	}
	return &ConnStringError{driver: driverMigrate, stage: stageRefused}
}

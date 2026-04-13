package rewrite

import (
	"regexp"
	"strings"
)

var dropDatabaseRe = regexp.MustCompile(
	`(?i)\bDROP\s+DATABASE\s+(?:IF\s+EXISTS\s+)?` +
		`("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`)

// ExtractDropDatabase returns the database name from a DROP DATABASE
// statement, or "" if the SQL is not such a statement. Identifier quoting is
// unwrapped.
func ExtractDropDatabase(sql string) string {
	m := dropDatabaseRe.FindStringSubmatch(sql)
	if m == nil {
		return ""
	}
	return UnquoteIdent(m[1])
}

// QuoteString wraps a string literal in single quotes, doubling embedded
// quotes. Use for SQL string literals built by the proxy.
func QuoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// BuildTerminate returns a SELECT that asks PostgreSQL to terminate every
// other backend currently connected to dbname. The proxy's own session is
// excluded via pg_backend_pid().
func BuildTerminate(dbname string) string {
	return "SELECT pg_terminate_backend(pid) FROM pg_stat_activity " +
		"WHERE datname = " + QuoteString(dbname) +
		" AND pid <> pg_backend_pid()"
}

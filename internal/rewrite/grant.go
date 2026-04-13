package rewrite

import (
	"regexp"
	"strings"
)

// (?i) makes the keyword match case-insensitive. The captured name is either
// a double-quoted identifier (with `""` as an escaped quote) or a plain
// unquoted identifier. CREATE USER MAPPING is filtered in ExtractCreateRole.
var createRoleRe = regexp.MustCompile(
	`(?i)\bCREATE\s+(ROLE|USER|GROUP)\s+(?:IF\s+NOT\s+EXISTS\s+)?` +
		`("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`)

// ExtractCreateRole returns the role name created by a CREATE
// ROLE/USER/GROUP statement, or "" if the SQL is not such a statement.
// CREATE USER MAPPING is recognized and ignored.
func ExtractCreateRole(sql string) string {
	m := createRoleRe.FindStringSubmatch(sql)
	if m == nil {
		return ""
	}
	keyword := strings.ToUpper(m[1])
	name := m[2]
	if keyword == "USER" && strings.EqualFold(name, "MAPPING") {
		return ""
	}
	return UnquoteIdent(name)
}

// QuoteIdent wraps an identifier in double quotes, doubling any embedded
// quotes. Use when constructing SQL fragments to forward to PostgreSQL.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// UnquoteIdent strips surrounding double quotes from a quoted identifier and
// unescapes embedded `""`. Unquoted identifiers are returned as-is.
func UnquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

// BuildGrant returns the SQL text granting role to master.
func BuildGrant(role, master string) string {
	return "GRANT " + QuoteIdent(role) + " TO " + QuoteIdent(master)
}

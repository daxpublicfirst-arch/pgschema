package pg

import (
	"fmt"
	"regexp"
	"strings"
)

// RefusedError is returned when a SQL statement, identifier or ORDER BY
// expression is rejected by the read-only rules. Callers map it to the
// READ_ONLY_REFUSED or QUERY_REFUSED error codes.
type RefusedError struct {
	Reason string
}

func (e *RefusedError) Error() string { return e.Reason }

// IsRefused reports whether err is a *RefusedError.
func IsRefused(err error) bool {
	_, ok := err.(*RefusedError)
	return ok
}

func refused(format string, args ...any) error {
	return &RefusedError{Reason: fmt.Sprintf(format, args...)}
}

// Statements that may start a user-supplied query. Anything else is refused.
var allowedFirstWords = map[string]bool{
	"select":  true,
	"with":    true,
	"explain": true,
	"show":    true,
}

// bannedWords are whole words that must never appear in user SQL, outside
// string literals, quoted identifiers and comments. They cover every write /
// DDL / session-mutating statement plus COMMENT and DISCARD, which are also
// writes against the server or the session.
var bannedWords = map[string]bool{
	"insert": true, "update": true, "delete": true, "merge": true,
	"create": true, "alter": true, "drop": true, "truncate": true,
	"grant": true, "revoke": true, "copy": true, "call": true,
	"do": true, "vacuum": true, "analyze": true, "analyse": true,
	"set": true, "reset": true, "comment": true, "discard": true,
}

// bannedFunctions are function names that must never appear in user SQL.
// They read or write server-side files, large objects, or other databases.
var bannedFunctions = map[string]bool{
	"lo_import": true, "lo_export": true,
	"pg_read_file": true, "pg_read_binary_file": true,
}

// ValidateSQL enforces the read-only rules for one user-supplied statement:
//   - it must start with SELECT, WITH, EXPLAIN or SHOW;
//   - it must be a single statement (one trailing semicolon at most);
//   - it must not contain a banned word, a banned function, COPY ... TO
//     PROGRAM, or SELECT ... INTO (which creates a table).
//
// The check runs on a small tokenizer that understands string literals
// (including E'...' escapes and dollar-quoted strings), quoted identifiers
// and line/block comments, so a banned word inside a string or a comment
// does not cause a refusal, and a semicolon inside a string does not look
// like a second statement.
func ValidateSQL(sql string) error {
	if strings.TrimSpace(sql) == "" {
		return refused("empty SQL statement")
	}
	toks, err := scanSQL(sql)
	if err != nil {
		return refused("could not parse SQL: %s", err)
	}

	// COPY ... TO PROGRAM runs an OS command; refuse it with a specific
	// message before the other checks (COPY alone is banned too).
	for i, t := range toks {
		if t.kind == tokWord && t.text == "copy" {
			for j := i + 1; j+1 < len(toks); j++ {
				if toks[j].kind == tokWord && toks[j].text == "to" &&
					toks[j+1].kind == tokWord && toks[j+1].text == "program" {
					return refused("COPY ... TO PROGRAM is not allowed: it runs an OS command on the server")
				}
			}
		}
	}

	// First significant word decides the statement kind.
	first := ""
	for _, t := range toks {
		if t.kind == tokWord {
			first = t.text
			break
		}
	}
	if first == "" {
		return refused("empty SQL statement")
	}
	if !allowedFirstWords[first] {
		return refused("statement type %q is not allowed: only SELECT, WITH, EXPLAIN and SHOW are read-only", strings.ToUpper(first))
	}

	// Multiple statements: any token after the first semicolon means there
	// is a second statement. A single trailing semicolon is allowed.
	for i, t := range toks {
		if t.kind == tokSemi {
			if i+1 < len(toks) {
				return refused("multiple statements are not allowed: supply exactly one statement")
			}
			break
		}
	}

	depth := 0
	for _, t := range toks {
		switch t.kind {
		case tokLParen:
			depth++
		case tokRParen:
			depth--
		case tokWord:
			lower := t.text
			if bannedWords[lower] {
				return refused("keyword %q is not allowed in read-only SQL", strings.ToUpper(lower))
			}
			if bannedFunctions[lower] || strings.HasPrefix(lower, "dblink") {
				return refused("function %q is not allowed in read-only SQL", lower)
			}
			// SELECT ... INTO creates a table. Only a top-level INTO
			// (outside any subquery) can do that.
			if lower == "into" && depth == 0 &&
				(first == "select" || first == "with" || first == "explain") {
				return refused("SELECT ... INTO is not allowed: it creates a table")
			}
		}
	}
	return nil
}

// identifierRe is the only shape a table name may have for `sample`:
// an unqualified PostgreSQL identifier. Dots (schema qualification),
// quotes, semicolons and whitespace are all rejected.
var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateIdentifier rejects any table name that is not a plain,
// unqualified identifier, so it can be safely quoted into a query.
func ValidateIdentifier(name string) error {
	if name == "" {
		return refused("table name must not be empty")
	}
	if !identifierRe.MatchString(name) {
		return refused("invalid table name %q: use a plain identifier without a dot, quote, semicolon or whitespace", name)
	}
	if len(name) > 63 {
		return refused("invalid table name %q: identifiers are at most 63 characters", name)
	}
	return nil
}

// ValidateSchemaName rejects any --schema value that is not a plain,
// unqualified identifier, using the same rule as ValidateIdentifier so
// it can be safely quoted into a query as "schema"."table".
func ValidateSchemaName(name string) error {
	if name == "" {
		return refused("schema name must not be empty")
	}
	if !identifierRe.MatchString(name) {
		return refused("invalid schema name %q: use a plain identifier without a dot, quote, semicolon or whitespace", name)
	}
	if len(name) > 63 {
		return refused("invalid schema name %q: identifiers are at most 63 characters", name)
	}
	return nil
}

// orderByPartRe allows one ORDER BY item: a plain column name with an
// optional ASC or DESC.
var orderByPartRe = regexp.MustCompile(`(?i)^[A-Za-z_][A-Za-z0-9_]*(\s+(ASC|DESC))?$`)

// ValidateOrderBy checks a --order value for `sample`: a comma-separated
// list of plain column names, each optionally followed by ASC or DESC.
// Anything else (expressions, functions, subqueries) is rejected, because
// the value is interpolated into ORDER BY.
func ValidateOrderBy(order string) error {
	order = strings.TrimSpace(order)
	if order == "" {
		return nil
	}
	for _, part := range strings.Split(order, ",") {
		part = strings.TrimSpace(part)
		if !orderByPartRe.MatchString(part) {
			return refused("invalid --order value %q: use a comma-separated list of column names, each optionally followed by ASC or DESC", part)
		}
	}
	return nil
}

// ValidateLimit enforces the row limit range: at least 1, at most 1000.
func ValidateLimit(limit int) error {
	if limit < 1 {
		return refused("limit must be at least 1, got %d", limit)
	}
	if limit > 1000 {
		return refused("limit must be at most 1000, got %d", limit)
	}
	return nil
}

// --- SQL tokenizer -------------------------------------------------------

type tokenKind int

const (
	tokWord        tokenKind = iota // bare word (lowercased in text)
	tokString                       // '...' literal, E'...' or $tag$...$tag$
	tokQuotedIdent                  // "..." identifier
	tokSemi                         // ;
	tokLParen                       // (
	tokRParen                       // )
	tokOther                        // any other single character
)

type sqlToken struct {
	kind tokenKind
	text string // lowercased word for tokWord, otherwise the raw text
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// scanSQL splits sql into significant tokens, skipping whitespace and
// comments and collapsing every string literal, dollar-quoted string and
// quoted identifier into a single opaque token.
func scanSQL(sql string) ([]sqlToken, error) {
	var toks []sqlToken
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			depth := 1
			i += 2
			for i < n && depth > 0 {
				if sql[i] == '/' && i+1 < n && sql[i+1] == '*' {
					depth++
					i += 2
				} else if sql[i] == '*' && i+1 < n && sql[i+1] == '/' {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth > 0 {
				return nil, fmt.Errorf("unterminated block comment")
			}
		case c == '\'':
			end, err := scanQuoted(sql, i, '\'', false)
			if err != nil {
				return nil, err
			}
			i = end
			toks = append(toks, sqlToken{kind: tokString})
		case c == '"':
			end, err := scanQuoted(sql, i, '"', false)
			if err != nil {
				return nil, err
			}
			i = end
			toks = append(toks, sqlToken{kind: tokQuotedIdent})
		case c == '$':
			// Dollar-quoted string: $tag$ ... $tag$ (tag may be empty).
			j := i + 1
			for j < n && (isLetter(sql[j]) || isDigit(sql[j]) || sql[j] == '_') {
				j++
			}
			if j < n && sql[j] == '$' {
				tag := sql[i : j+1]
				rest := strings.Index(sql[j+1:], tag)
				if rest < 0 {
					return nil, fmt.Errorf("unterminated dollar-quoted string")
				}
				i = j + 1 + rest + len(tag)
				toks = append(toks, sqlToken{kind: tokString})
			} else {
				// $1, $2 ... parameter placeholder.
				i++
				toks = append(toks, sqlToken{kind: tokOther, text: "$"})
			}
		case c == ';':
			toks = append(toks, sqlToken{kind: tokSemi, text: ";"})
			i++
		case c == '(':
			toks = append(toks, sqlToken{kind: tokLParen, text: "("})
			i++
		case c == ')':
			toks = append(toks, sqlToken{kind: tokRParen, text: ")"})
			i++
		case isLetter(c) || c == '_':
			j := i
			for j < n && (isLetter(sql[j]) || isDigit(sql[j]) || sql[j] == '_' || sql[j] == '$') {
				j++
			}
			word := sql[i:j]
			// E'...' escape string: backslash escapes apply.
			if (word == "E" || word == "e") && j < n && sql[j] == '\'' {
				end, err := scanQuoted(sql, j, '\'', true)
				if err != nil {
					return nil, err
				}
				i = end
				toks = append(toks, sqlToken{kind: tokString})
				continue
			}
			toks = append(toks, sqlToken{kind: tokWord, text: strings.ToLower(word)})
			i = j
		default:
			i++
			toks = append(toks, sqlToken{kind: tokOther, text: string(c)})
		}
	}
	return toks, nil
}

// scanQuoted consumes a '...' or "..." literal starting at start and
// returns the index just past its closing quote. A doubled quote is an
// escaped quote; when escapes is true, backslash also escapes the next
// character (E'...' strings).
func scanQuoted(sql string, start int, quote byte, escapes bool) (int, error) {
	i := start + 1
	for i < len(sql) {
		if escapes && sql[i] == '\\' {
			i += 2
			continue
		}
		if sql[i] == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i += 2
				continue
			}
			return i + 1, nil
		}
		i++
	}
	return 0, fmt.Errorf("unterminated string literal")
}

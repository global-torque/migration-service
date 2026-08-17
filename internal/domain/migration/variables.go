package migration

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var migrationVariablePattern = regexp.MustCompile(`\$\{(MIGRATION_[A-Z0-9_]+)\}`)

type sqlLexicalState uint8

const (
	sqlStateNormal sqlLexicalState = iota
	sqlStateSingleQuotedString
	sqlStateDoubleQuotedIdentifier
	sqlStateLineComment
	sqlStateBlockComment
	sqlStateDollarQuotedBody
)

func (s sqlLexicalState) String() string {
	switch s {
	case sqlStateSingleQuotedString:
		return "a single-quoted string"
	case sqlStateDoubleQuotedIdentifier:
		return "a double-quoted identifier"
	case sqlStateLineComment:
		return "a line comment"
	case sqlStateBlockComment:
		return "a block comment"
	case sqlStateDollarQuotedBody:
		return "a dollar-quoted body"
	default:
		return "SQL"
	}
}

type migrationVariableExecutionError struct {
	cause     error
	variables []string
}

func (e *migrationVariableExecutionError) Error() string {
	return fmt.Sprintf(
		"PostgreSQL execution failed for migration variables %s; database error details redacted",
		strings.Join(e.variables, ", "),
	)
}

func (e *migrationVariableExecutionError) Unwrap() error {
	return e.cause
}

// expandMigrationVariables replaces MIGRATION_* placeholders with PostgreSQL
// string literals. The placeholder must not be surrounded by SQL quotes.
func expandMigrationVariables(query string) (string, error) {
	matches := migrationVariablePattern.FindAllStringSubmatchIndex(query, -1)
	if len(matches) == 0 {
		return query, nil
	}
	if err := validateMigrationVariablePositions(query, matches); err != nil {
		return "", err
	}

	values := make(map[string]string, len(matches))
	missing := make([]string, 0)
	seen := make(map[string]struct{}, len(matches))

	for _, match := range matches {
		name := query[match[2]:match[3]]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		value, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		if strings.IndexByte(value, 0) >= 0 {
			return "", fmt.Errorf("migration variable %s contains a NUL byte", name)
		}
		values[name] = value
	}

	if len(missing) > 0 {
		return "", fmt.Errorf("migration variables are not set: %s", strings.Join(missing, ", "))
	}

	expanded := migrationVariablePattern.ReplaceAllStringFunc(query, func(placeholder string) string {
		name := placeholder[2 : len(placeholder)-1]
		return postgresStringLiteral(query, values[name])
	})

	return expanded, nil
}

// validateMigrationVariablePositions accepts placeholders only as standalone
// SQL value tokens. In particular, it prevents a generated dollar-quote tag
// from being absorbed into an adjacent PostgreSQL identifier and prevents
// values containing newlines or quote delimiters from escaping comments or
// quoted regions.
func validateMigrationVariablePositions(query string, matches [][]int) error {
	state := sqlStateNormal
	blockCommentDepth := 0
	dollarDelimiter := ""
	singleQuoteBackslashEscapes := false
	matchIndex := 0

	for i := 0; i < len(query); {
		if matchIndex < len(matches) && matches[matchIndex][0] <= i {
			match := matches[matchIndex]
			start := match[0]
			end := match[1]
			name := query[match[2]:match[3]]
			if state != sqlStateNormal {
				return fmt.Errorf(
					"migration variable %s must be an unquoted standalone SQL value; found inside %s",
					name,
					state,
				)
			}
			if start < i ||
				(start > 0 && isPostgresIdentifierByte(query[start-1])) ||
				(end < len(query) && isPostgresIdentifierByte(query[end])) {
				return fmt.Errorf(
					"migration variable %s must be separated from SQL identifiers",
					name,
				)
			}

			i = end
			matchIndex++
			continue
		}

		switch state {
		case sqlStateNormal:
			switch {
			case strings.HasPrefix(query[i:], "--"):
				state = sqlStateLineComment
				i += 2
			case strings.HasPrefix(query[i:], "/*"):
				state = sqlStateBlockComment
				blockCommentDepth = 1
				i += 2
			case query[i] == '\'':
				state = sqlStateSingleQuotedString
				singleQuoteBackslashEscapes = postgresEscapeStringPrefix(query, i)
				i++
			case query[i] == '"':
				state = sqlStateDoubleQuotedIdentifier
				i++
			default:
				if delimiter := postgresDollarQuoteDelimiter(query, i); delimiter != "" {
					state = sqlStateDollarQuotedBody
					dollarDelimiter = delimiter
					i += len(delimiter)
				} else {
					i++
				}
			}

		case sqlStateSingleQuotedString:
			switch {
			case query[i] == '\\' && !singleQuoteBackslashEscapes:
				name := query[matches[0][2]:matches[0][3]]
				return fmt.Errorf(
					"migration variable %s cannot be safely validated with a backslash in an ordinary SQL string; use an E-prefixed string",
					name,
				)
			case query[i] == '\\' && i+1 < len(query):
				i += 2
			case query[i] == '\'' && i+1 < len(query) && query[i+1] == '\'':
				i += 2
			case query[i] == '\'':
				state = sqlStateNormal
				singleQuoteBackslashEscapes = false
				i++
			default:
				i++
			}

		case sqlStateDoubleQuotedIdentifier:
			switch {
			case query[i] == '"' && i+1 < len(query) && query[i+1] == '"':
				i += 2
			case query[i] == '"':
				state = sqlStateNormal
				i++
			default:
				i++
			}

		case sqlStateLineComment:
			if query[i] == '\n' || query[i] == '\r' {
				state = sqlStateNormal
			}
			i++

		case sqlStateBlockComment:
			switch {
			case strings.HasPrefix(query[i:], "/*"):
				blockCommentDepth++
				i += 2
			case strings.HasPrefix(query[i:], "*/"):
				blockCommentDepth--
				i += 2
				if blockCommentDepth == 0 {
					state = sqlStateNormal
				}
			default:
				i++
			}

		case sqlStateDollarQuotedBody:
			if strings.HasPrefix(query[i:], dollarDelimiter) {
				state = sqlStateNormal
				i += len(dollarDelimiter)
				dollarDelimiter = ""
			} else {
				i++
			}
		}
	}

	return nil
}

func postgresDollarQuoteDelimiter(query string, start int) string {
	if query[start] != '$' || start+1 >= len(query) {
		return ""
	}
	if start > 0 && isPostgresIdentifierByte(query[start-1]) {
		return ""
	}

	if query[start+1] == '$' {
		return "$$"
	}
	if !isPostgresTagStart(query[start+1]) {
		return ""
	}

	for i := start + 2; i < len(query); i++ {
		if query[i] == '$' {
			return query[start : i+1]
		}
		if !isPostgresTagByte(query[i]) {
			return ""
		}
	}

	return ""
}

func postgresEscapeStringPrefix(query string, quote int) bool {
	if quote == 0 || query[quote-1] != 'e' && query[quote-1] != 'E' {
		return false
	}
	return quote == 1 || !isPostgresIdentifierByte(query[quote-2])
}

func isPostgresTagStart(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

func isPostgresTagByte(b byte) bool {
	return isPostgresTagStart(b) || b >= '0' && b <= '9'
}

func isPostgresIdentifierByte(b byte) bool {
	return isPostgresTagByte(b) || b == '$'
}

// postgresStringLiteral uses a delimiter that occurs in neither the migration
// nor the value, so arbitrary values cannot terminate the generated literal.
func postgresStringLiteral(query, value string) string {
	for suffix := 0; ; suffix++ {
		delimiter := "$migration_variable$"
		if suffix > 0 {
			delimiter = fmt.Sprintf("$migration_variable_%d$", suffix)
		}

		if !strings.Contains(query, delimiter) && !strings.Contains(value, delimiter) {
			return delimiter + value + delimiter
		}
	}
}

// redactMigrationVariableExecutionError prevents PostgreSQL error details from
// echoing resolved values into migration-service logs. Callers can still use
// errors.Is/errors.As to inspect the wrapped database error programmatically.
func redactMigrationVariableExecutionError(query string, err error) error {
	if err == nil {
		return nil
	}

	matches := migrationVariablePattern.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return err
	}

	variables := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		name := match[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		variables = append(variables, name)
	}

	return &migrationVariableExecutionError{
		cause:     err,
		variables: variables,
	}
}

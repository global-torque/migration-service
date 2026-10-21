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

// expandMigrationVariables replaces MIGRATION_* placeholders with their raw
// environment values in a single pass. The placeholder may occur in any SQL
// lexical context.
func expandMigrationVariables(query string) (string, error) {
	matches := migrationVariablePattern.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return query, nil
	}

	values := make(map[string]string, len(matches))
	missing := make([]string, 0)
	seen := make(map[string]struct{}, len(matches))

	for _, match := range matches {
		name := match[1]
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
		return values[name]
	})

	return expanded, nil
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

package migration

import (
	"fmt"
	"regexp"
	"strings"
)

const environmentIdentifierPlaceholder = "{{ ENV_NAME }}"

var environmentIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// renderEnvironmentIdentifier replaces the one supported identifier template
// immediately before migration execution. The source query remains the input
// to migration hashing, logging, and --final-sql output.
func renderEnvironmentIdentifier(query, envName string) (string, error) {
	hasOpeningMarker := strings.Contains(query, "{{")
	hasClosingMarker := strings.Contains(query, "}}")
	if !hasOpeningMarker && !hasClosingMarker {
		return query, nil
	}

	if envName == "" {
		return "", fmt.Errorf("ENV_NAME is not set for identifier rendering")
	}
	if !environmentIdentifierPattern.MatchString(envName) {
		return "", fmt.Errorf("ENV_NAME must match [a-z][a-z0-9_]* for identifier rendering")
	}

	withoutKnownMarkers := strings.ReplaceAll(query, environmentIdentifierPlaceholder, "")
	if strings.Contains(withoutKnownMarkers, "{{") || strings.Contains(withoutKnownMarkers, "}}") {
		return "", fmt.Errorf("migration contains an unknown or nested identifier template; only {{ ENV_NAME }} is supported")
	}

	var rendered strings.Builder
	rendered.Grow(len(query))
	renderedMarkerOffsets := make([]int, 0, strings.Count(query, environmentIdentifierPlaceholder))

	state := sqlStateNormal
	blockCommentDepth := 0
	dollarDelimiter := ""
	singleQuoteBackslashEscapes := false

	for i := 0; i < len(query); {
		if strings.HasPrefix(query[i:], environmentIdentifierPlaceholder) {
			if state != sqlStateNormal {
				return "", fmt.Errorf(
					"{{ ENV_NAME }} must be part of an unquoted SQL identifier; found inside %s",
					state,
				)
			}

			renderedMarkerOffsets = append(renderedMarkerOffsets, rendered.Len())
			rendered.WriteString(envName)
			i += len(environmentIdentifierPlaceholder)
			continue
		}

		switch state {
		case sqlStateNormal:
			switch {
			case strings.HasPrefix(query[i:], "--"):
				state = sqlStateLineComment
				rendered.WriteString("--")
				i += 2
			case strings.HasPrefix(query[i:], "/*"):
				state = sqlStateBlockComment
				blockCommentDepth = 1
				rendered.WriteString("/*")
				i += 2
			case query[i] == '\'':
				state = sqlStateSingleQuotedString
				singleQuoteBackslashEscapes = postgresEscapeStringPrefix(query, i)
				rendered.WriteByte(query[i])
				i++
			case query[i] == '"':
				state = sqlStateDoubleQuotedIdentifier
				rendered.WriteByte(query[i])
				i++
			default:
				if delimiter := postgresDollarQuoteDelimiter(query, i); delimiter != "" {
					state = sqlStateDollarQuotedBody
					dollarDelimiter = delimiter
					rendered.WriteString(delimiter)
					i += len(delimiter)
				} else {
					rendered.WriteByte(query[i])
					i++
				}
			}

		case sqlStateSingleQuotedString:
			switch {
			case query[i] == '\\' && !singleQuoteBackslashEscapes:
				return "", fmt.Errorf(
					"{{ ENV_NAME }} cannot be safely validated with a backslash in an ordinary SQL string; use an E-prefixed string",
				)
			case query[i] == '\\' && singleQuoteBackslashEscapes && i+1 < len(query):
				rendered.WriteString(query[i : i+2])
				i += 2
			case query[i] == '\'' && i+1 < len(query) && query[i+1] == '\'':
				rendered.WriteString(query[i : i+2])
				i += 2
			case query[i] == '\'':
				state = sqlStateNormal
				singleQuoteBackslashEscapes = false
				rendered.WriteByte(query[i])
				i++
			default:
				rendered.WriteByte(query[i])
				i++
			}

		case sqlStateDoubleQuotedIdentifier:
			if query[i] == '"' && i+1 < len(query) && query[i+1] == '"' {
				rendered.WriteString(query[i : i+2])
				i += 2
			} else {
				if query[i] == '"' {
					state = sqlStateNormal
				}
				rendered.WriteByte(query[i])
				i++
			}

		case sqlStateLineComment:
			rendered.WriteByte(query[i])
			if query[i] == '\n' || query[i] == '\r' {
				state = sqlStateNormal
			}
			i++

		case sqlStateBlockComment:
			switch {
			case strings.HasPrefix(query[i:], "/*"):
				blockCommentDepth++
				rendered.WriteString("/*")
				i += 2
			case strings.HasPrefix(query[i:], "*/"):
				blockCommentDepth--
				rendered.WriteString("*/")
				i += 2
				if blockCommentDepth == 0 {
					state = sqlStateNormal
				}
			default:
				rendered.WriteByte(query[i])
				i++
			}

		case sqlStateDollarQuotedBody:
			if strings.HasPrefix(query[i:], dollarDelimiter) {
				state = sqlStateNormal
				rendered.WriteString(dollarDelimiter)
				i += len(dollarDelimiter)
				dollarDelimiter = ""
			} else {
				rendered.WriteByte(query[i])
				i++
			}
		}
	}

	renderedQuery := rendered.String()
	for _, markerOffset := range renderedMarkerOffsets {
		identifierStart := markerOffset
		for identifierStart > 0 && isPostgresIdentifierByte(renderedQuery[identifierStart-1]) {
			identifierStart--
		}
		identifierEnd := markerOffset + len(envName)
		for identifierEnd < len(renderedQuery) && isPostgresIdentifierByte(renderedQuery[identifierEnd]) {
			identifierEnd++
		}
		if generatedLength := identifierEnd - identifierStart; generatedLength > 63 {
			return "", fmt.Errorf("identifier generated with {{ ENV_NAME }} is %d bytes; PostgreSQL limit is 63", generatedLength)
		}
	}

	return renderedQuery, nil
}

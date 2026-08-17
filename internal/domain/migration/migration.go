package migration

import (
	"crypto/md5"
	"fmt"
	"strings"
)

// Migration is a single migration.
type Migration struct {
	AllowError bool
	NoAuto     bool
	EnvRegex   string
	Path       string
	Query      string
	Hash       string
}

func NewMigration(query string, path string) Migration {
	hash := fmt.Sprintf("%x", md5.Sum([]byte(query)))

	mig := Migration{
		AllowError: false,
		EnvRegex:   "",
		Query:      query,
		Path:       path,
		Hash:       hash,
	}

	lines := strings.Split(query, "\n")
	for _, line := range lines {
		line = strings.Replace(line, "\t", "", -1)
		// we don't have any comments at all
		if len(line) < 2 || line[0:2] != "--" {
			break
		}
		if len(line) > 15 && line[0:2] == "--" {
			comment := line[2:]
			comment = strings.Replace(comment, " ", "", -1)
			comment = strings.Replace(comment, "-", "", -1)
			pairs := strings.Split(comment, ",")
			for _, pair := range pairs {
				vals := strings.Split(pair, ":")
				if vals[0] == "allow_error" {
					if vals[1] == "true" || vals[1] == "1" {
						mig.AllowError = true
						break
					}
				} else if vals[0] == "required_env" {
					mig.EnvRegex = strings.Trim(vals[1], " ")
					break
				}
			}
		}
	}
	return mig
}

package pg

import "regexp"

// dsnPasswordRe matches the user:password part of a URL-style DSN:
// postgres://user:password@host/db
var dsnPasswordRe = regexp.MustCompile(`://([^:/@\s]+):[^@\s]*@`)

// dsnKeywordPasswordRe matches the password in a keyword/value DSN:
// host=... password=secret ...
var dsnKeywordPasswordRe = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|\S+)`)

// RedactDSN removes the password from a DSN (URL or keyword/value form) so
// it can never be printed in an error, telemetry line or output file.
// A DSN without a password is returned unchanged.
func RedactDSN(s string) string {
	s = dsnPasswordRe.ReplaceAllString(s, "://$1:***@")
	s = dsnKeywordPasswordRe.ReplaceAllString(s, "${1}***")
	return s
}

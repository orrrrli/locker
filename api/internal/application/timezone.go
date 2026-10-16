package application

import (
	"regexp"
	"time"
)

// ianaZone is the shape of a canonical zone name: Area/Location, maybe with
// a sub-location (America/Argentina/Buenos_Aires). LoadLocation alone also
// accepts "", "Local", "posixrules", "Factory" and uncleaned paths like
// "America/./Tijuana", which Postgres's AT TIME ZONE rejects.
var ianaZone = regexp.MustCompile(`^[A-Z][A-Za-z]*(/[A-Za-z0-9_+-]+){1,2}$`)

// ValidTimezone accepts only IANA zone names that load (R6.7, R15.2).
func ValidTimezone(tz string) bool {
	if !ianaZone.MatchString(tz) {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

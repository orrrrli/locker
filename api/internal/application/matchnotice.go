package application

import (
	"fmt"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// Titles of the automatic match notices (R11.4).
const (
	MatchCreatedTitle   = "Partido nuevo"
	MatchChangedTitle   = "Cambio de partido"
	MatchCancelledTitle = "Partido cancelado"
)

// shortDays are the Spanish day abbreviations, indexed by time.Weekday.
var shortDays = [...]string{"Dom", "Lun", "Mar", "Mié", "Jue", "Vie", "Sáb"}

// MatchNotice is the automatic notice about m for its team: "<title>: vs.
// Halcones Sur" over "Dom 12 · 9:00 · Cancha 2.", with the time in tz.
func MatchNotice(kind domain.NotificationKind, title string, m domain.Match, tz string) domain.Notification {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC // zones are validated on write; never expected
	}
	t := m.StartsAt.In(loc)
	id := m.ID
	return domain.Notification{
		TeamID:  m.TeamID,
		Kind:    kind,
		Title:   title + ": vs. " + m.RivalName,
		Body:    fmt.Sprintf("%s %d · %d:%02d · %s.", shortDays[t.Weekday()], t.Day(), t.Hour(), t.Minute(), strings.TrimRight(m.Location, ".")),
		MatchID: &id,
	}
}

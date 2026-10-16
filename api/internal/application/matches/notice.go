package matches

import (
	"fmt"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// shortDays are the Spanish day abbreviations, indexed by time.Weekday.
var shortDays = [...]string{"Dom", "Lun", "Mar", "Mié", "Jue", "Vie", "Sáb"}

// matchNotice is the automatic notice for a created or changed match (R11.4):
// "Partido nuevo: vs. Halcones Sur" over "Dom 12 · 9:00 · Cancha 2.", with
// the time in the team's zone.
func matchNotice(kind domain.NotificationKind, m domain.Match, tz string) domain.Notification {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC // the team's zone was validated on write; never expected
	}
	t := m.StartsAt.In(loc)
	title := "Partido nuevo: vs. " + m.RivalName
	if kind == domain.NotificationMatchChanged {
		title = "Cambio de partido: vs. " + m.RivalName
	}
	id := m.ID
	return domain.Notification{
		TeamID:  m.TeamID,
		Kind:    kind,
		Title:   title,
		Body:    fmt.Sprintf("%s %d · %d:%02d · %s.", shortDays[t.Weekday()], t.Day(), t.Hour(), t.Minute(), strings.TrimRight(m.Location, ".")),
		MatchID: &id,
	}
}

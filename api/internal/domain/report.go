package domain

import "time"

// ReportReason is why a user reported a post (R23.1): a fixed list.
type ReportReason string

const (
	ReportReasonSpam          ReportReason = "spam"
	ReportReasonInappropriate ReportReason = "inappropriate"
	ReportReasonHarassment    ReportReason = "harassment"
	ReportReasonOther         ReportReason = "other"
)

// Valid reports whether r is one of the four reasons. The report table's
// CHECK constraint backs it.
func (r ReportReason) Valid() bool {
	switch r {
	case ReportReasonSpam, ReportReasonInappropriate, ReportReasonHarassment, ReportReasonOther:
		return true
	}
	return false
}

// Report is one user's report of one post (R23.1). Reason is from the fixed
// list; Note is optional. The operator's removal resolves it (R23.9).
type Report struct {
	ID        int64
	PostID    int64
	Reason    ReportReason
	Note      *string
	CreatedAt time.Time
}

// QueuedReport is one open report in the operator's queue (R23.4): the report,
// the post it is about with the fields the operator needs to judge it, and how
// many open reports that post has.
type QueuedReport struct {
	ID             int64
	Post           Post
	Reason         ReportReason
	Note           *string
	ReportersCount int
	CreatedAt      time.Time
}

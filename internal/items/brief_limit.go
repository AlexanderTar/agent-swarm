package items

// ErrBriefTooLong is the §17.3 copy for an over-long brief. It is the one
// message for every place the cap applies: an item's brief at write time, and
// a rendered kickoff brief at spawn time (internal/runtime).
const ErrBriefTooLong = "Brief too long (max 6000 characters). Move detail into an artifact and reference it."

// MaxBrief is the brief cap, measured in bytes of the text written or rendered.
const MaxBrief = 6000

// BriefFits reports whether a brief is within MaxBrief. Write paths and every
// kickoff builder call this, so coder and reviewer kickoffs can't disagree on
// the limit (BUG-106).
func BriefFits(brief string) bool { return len(brief) <= MaxBrief }

// ValidateBrief refuses a brief over MaxBrief with ErrBriefTooLong.
func ValidateBrief(brief string) error {
	if !BriefFits(brief) {
		return &Error{Code: CodeBadRequest, Message: ErrBriefTooLong}
	}
	return nil
}

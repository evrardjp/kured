package blockers

type BlockingChecker struct {
	blocking bool
}

func (fbc BlockingChecker) IsBlocked() bool {
	return fbc.blocking
}

// TODO: Test IsBlocked() for each blocker.

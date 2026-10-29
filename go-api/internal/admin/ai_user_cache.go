package admin

// InvalidateAIUserSessions is called after an approved user change commits.
// Session-row deletion itself is performed in the same write transaction.
func (s *DBService) InvalidateAIUserSessions(userID int64) {
	if s.authCache != nil {
		s.authCache.InvalidateUser(userID)
	}
}

// AIPlanTrafficBytes preserves legacy GiB and already-byte plan storage semantics.
func AIPlanTrafficBytes(value int64) int64 { return planTransferEnableBytes(value) }

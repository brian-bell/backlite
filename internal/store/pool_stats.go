package store

// PoolStats holds a snapshot of database connection statistics.
type PoolStats struct {
	AcquiredConns int32
	IdleConns     int32
	TotalConns    int32
	MaxConns      int32
}

// PoolStats returns a snapshot of the underlying database handle statistics.
func (s *SQLiteStore) PoolStats() PoolStats {
	st := s.db.Stats()
	return PoolStats{
		AcquiredConns: int32(st.InUse),
		IdleConns:     int32(st.Idle),
		TotalConns:    int32(st.OpenConnections),
		MaxConns:      int32(st.MaxOpenConnections),
	}
}

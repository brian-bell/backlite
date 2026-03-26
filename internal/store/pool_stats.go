package store

// PoolStats holds a snapshot of connection pool statistics.
type PoolStats struct {
	AcquiredConns int32
	IdleConns     int32
	TotalConns    int32
	MaxConns      int32
}

// PoolStats returns a snapshot of the underlying connection pool statistics.
func (s *PostgresStore) PoolStats() PoolStats {
	st := s.pool.Stat()
	return PoolStats{
		AcquiredConns: st.AcquiredConns(),
		IdleConns:     st.IdleConns(),
		TotalConns:    st.TotalConns(),
		MaxConns:      st.MaxConns(),
	}
}

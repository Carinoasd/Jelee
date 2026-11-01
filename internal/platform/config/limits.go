package config

// Process-wide database and delivery bounds.
const (
	// defaultMaxConnections sizes the PostgreSQL pool. Each running job,
	// background lease and request holds a connection while it queries;
	// 64 keeps a library scan from starving the API on one instance and
	// stays below PostgreSQL's default max_connections (100).
	defaultMaxConnections = 64
	// maxConnectionsLimit and maxStreamsLimit are sanity bounds, not tuning
	// advice: the database's max_connections and the host decide the rest.
	maxConnectionsLimit = 1024
	maxStreamsLimit     = 1024
	defaultMaxStreams   = 8
)

package gateway

type Manager struct {
	// mapping from principle to list of active sessions
	sessions map[string][]*Session
}

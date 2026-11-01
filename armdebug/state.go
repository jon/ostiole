package armdebug

// RestorationPossible reports whether the retained probe can still communicate
// for restoration. It sends no traffic and does not validate borrowed clients.
// Nil and released connections report false.
func (c *Conn) RestorationPossible() bool {
	return c != nil && c.probe != nil && c.probe.RestorationPossible()
}

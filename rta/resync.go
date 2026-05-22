package rta

import "time"

// notifyResync delivers an RTA resync signal to subscriptions that know how to
// refresh their backing resource.
func (c *Conn) notifyResync() {
	if !c.resyncReady() {
		c.log.Debug("ignored RTA resync during post-connect suppression window")
		return
	}

	for _, subscription := range c.subscriptionsForReconnect() {
		handler, ok := subscription.handler().(ResyncHandler)
		if ok {
			go handler.HandleResync()
		}
	}
}

// suppressResyncFor suppresses RTA resync delivery until d has elapsed.
func (c *Conn) suppressResyncFor(d time.Duration) {
	c.resyncMu.Lock()
	c.resyncReadyAt = time.Now().Add(d)
	c.resyncMu.Unlock()
}

// resyncReady reports whether post-connect resync suppression has elapsed.
func (c *Conn) resyncReady() bool {
	c.resyncMu.RLock()
	readyAt := c.resyncReadyAt
	c.resyncMu.RUnlock()
	return readyAt.IsZero() || time.Now().After(readyAt)
}

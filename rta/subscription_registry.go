package rta

import "sync"

// subscriptionRegistry routes RTA event subscription IDs to subscription state.
type subscriptionRegistry struct {
	mu   sync.RWMutex
	byID map[uint32]*Subscription
}

func newSubscriptionRegistry() subscriptionRegistry {
	return subscriptionRegistry{byID: make(map[uint32]*Subscription)}
}

// list returns the deduplicated subscriptions currently registered for event routing.
func (r *subscriptionRegistry) list() []*Subscription {
	r.mu.RLock()
	defer r.mu.RUnlock()

	subscriptions := make([]*Subscription, 0, len(r.byID))
	seen := make(map[*Subscription]struct{}, len(r.byID))
	for _, subscription := range r.byID {
		if _, ok := seen[subscription]; ok {
			continue
		}
		seen[subscription] = struct{}{}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions
}

func (r *subscriptionRegistry) get(id uint32) (*Subscription, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub, ok := r.byID[id]
	return sub, ok
}

// update replaces any stale routing entry for subscription with id from the
// latest successful subscribe handshake.
func (r *subscriptionRegistry) update(subscription *Subscription, id uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for existingID, existingSubscription := range r.byID {
		if existingSubscription == subscription {
			delete(r.byID, existingID)
		}
	}
	r.byID[id] = subscription
}

// remove removes every routing entry pointing to subscription and marks it inactive.
func (r *subscriptionRegistry) remove(subscription *Subscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, existingSubscription := range r.byID {
		if existingSubscription == subscription {
			delete(r.byID, id)
		}
	}
	subscription.deactivate()
}

// subscriptionsForReconnect collects the deduplicated set of subscriptions
// that need to be re-established on the current connection.
func (c *Conn) subscriptionsForReconnect() []*Subscription {
	return c.subscriptions.list()
}

// updateSubscriptionID replaces any stale routing entry for subscription with
// id from the latest successful subscribe handshake.
func (c *Conn) updateSubscriptionID(subscription *Subscription, id uint32) {
	c.subscriptions.update(subscription, id)
}

// removeSubscription removes every routing entry pointing to subscription.
func (c *Conn) removeSubscription(subscription *Subscription) {
	c.subscriptions.remove(subscription)
}

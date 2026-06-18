package internal

import (
	"context"

	"github.com/df-mc/go-xsapi/v2/rta"
)

// RTASubscriber is the part of [rta.Conn] needed to create subscriptions.
type RTASubscriber interface {
	Subscribe(context.Context, *rta.Subscription) error
}

// RTAUnsubscriber is the part of [rta.Conn] needed to remove subscriptions.
type RTAUnsubscriber interface {
	Unsubscribe(context.Context, *rta.Subscription) error
}

// Subscriber adapts conn to [RTASubscriber]. If conn is nil, the returned
// subscriber fails subscription attempts with [rta.ErrUnavailable].
func Subscriber(conn *rta.Conn) RTASubscriber {
	if conn == nil {
		return unavailableRTA{}
	}
	return conn
}

// Unsubscriber adapts conn to [RTAUnsubscriber]. If conn is nil, the returned
// unsubscriber fails unsubscribe attempts with [rta.ErrUnavailable].
func Unsubscriber(conn *rta.Conn) RTAUnsubscriber {
	if conn == nil {
		return unavailableRTA{}
	}
	return conn
}

type unavailableRTA struct{}

func (unavailableRTA) Subscribe(context.Context, *rta.Subscription) error {
	return rta.ErrUnavailable
}

func (unavailableRTA) Unsubscribe(context.Context, *rta.Subscription) error {
	return rta.ErrUnavailable
}

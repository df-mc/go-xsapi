package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/google/uuid"
)

type fakeUnsubscriber struct {
	attempts int
	failures int
	called   chan struct{}
	release  chan struct{}
}

func (f *fakeUnsubscriber) Unsubscribe(context.Context, *rta.Subscription) error {
	f.attempts++
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		<-f.release
	}
	if f.attempts <= f.failures {
		return errors.New("unsubscribe failed")
	}
	return nil
}

func TestClientCloseContextPreservesSubscriptionOnUnsubscribeError(t *testing.T) {
	subscription := &rta.Subscription{}
	subscriptionData := &subscriptionData{ConnectionID: uuid.New()}
	unsub := &fakeUnsubscriber{failures: 1}
	client := &Client{
		subscription:     subscription,
		subscriptionData: subscriptionData,
		unsub:            unsub,
	}

	if err := client.CloseContext(context.Background()); err == nil {
		t.Fatal("expected unsubscribe error")
	}
	if client.subscription != subscription {
		t.Fatal("subscription was cleared after unsubscribe failure")
	}
	if client.subscriptionData != subscriptionData {
		t.Fatal("subscription data was cleared after unsubscribe failure")
	}

	if err := client.CloseContext(context.Background()); err != nil {
		t.Fatalf("retry close returned error: %v", err)
	}
	if client.subscription != nil {
		t.Fatal("subscription was not cleared after successful retry")
	}
	if client.subscriptionData != nil {
		t.Fatal("subscription data was not cleared after successful retry")
	}
	if unsub.attempts != 2 {
		t.Fatalf("unsubscribe attempts = %d, want 2", unsub.attempts)
	}
}

func TestSubscriptionHandlerReconnectFailureClearsCachedSubscription(t *testing.T) {
	subscription := &rta.Subscription{}
	subscriptionData := &subscriptionData{ConnectionID: uuid.New()}
	client := &Client{
		subscription:     subscription,
		subscriptionData: subscriptionData,
	}
	handler := &subscriptionHandler{Client: client}

	handler.HandleReconnect(errors.New("reconnect failed"))

	if client.subscription != nil {
		t.Fatal("subscription was not cleared")
	}
	if client.subscriptionData != nil {
		t.Fatal("subscription data was not cleared")
	}
}

func TestClientCloseContextDoesNotHoldSubscriptionMuDuringUnsubscribe(t *testing.T) {
	subscription := &rta.Subscription{}
	unsub := &fakeUnsubscriber{
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	client := &Client{
		subscription:     subscription,
		subscriptionData: &subscriptionData{ConnectionID: uuid.New()},
		unsub:            unsub,
	}
	done := make(chan error, 1)
	go func() {
		done <- client.CloseContext(context.Background())
	}()

	select {
	case <-unsub.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unsubscribe")
	}

	handlerDone := make(chan struct{})
	go func() {
		handler := &subscriptionHandler{Client: client}
		handler.HandleReconnect(errors.New("reconnect failed"))
		close(handlerDone)
	}()

	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("HandleReconnect blocked behind CloseContext unsubscribe")
	}
	close(unsub.release)
	if err := <-done; err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
}

func TestSubscriptionHandlerReconnectRefreshesSessionConnectionID(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "session",
	}
	oldConnectionID := uuid.New()
	newConnectionID := uuid.New()
	requests := make(chan SessionDescription, 1)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var request SessionDescription
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		requests <- request
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body: io.NopCloser(bytes.NewReader([]byte(`{
				"properties": {},
				"members": {
					"me": {
						"properties": {
							"system": {
								"active": true,
								"connection": "` + newConnectionID.String() + `"
							}
						}
					}
				}
			}`))),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})}
	client := &Client{
		client: httpClient,
		sessions: map[string]*Session{
			ref.URL().String(): {
				client: &Client{client: httpClient},
				ref:    ref,
				cache: SessionDescription{
					Members: map[string]*MemberDescription{
						"me": {
							Properties: &MemberProperties{
								System: &MemberPropertiesSystem{
									Active:     true,
									Connection: oldConnectionID,
								},
							},
						},
					},
				},
				closed: make(chan struct{}),
			},
		},
	}
	handler := &subscriptionHandler{Client: client}

	handler.refreshSessionConnections(newConnectionID)

	select {
	case request := <-requests:
		got := request.Members["me"].Properties.System
		if !got.Active {
			t.Fatal("member was not marked active")
		}
		if got.Connection != newConnectionID {
			t.Fatalf("connection ID = %v, want %v", got.Connection, newConnectionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session connection update")
	}
}

func TestSubscriptionHandlerReconnectUsesCurrentSubscriptionCustom(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "session",
	}
	oldConnectionID := uuid.New()
	newConnectionID := uuid.New()
	requests := make(chan SessionDescription, 1)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var request SessionDescription
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		requests <- request
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body: io.NopCloser(bytes.NewReader([]byte(`{
				"properties": {},
				"members": {
					"me": {
						"properties": {
							"system": {
								"active": true,
								"connection": "` + newConnectionID.String() + `"
							}
						}
					}
				}
			}`))),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})}
	session := &Session{
		client: &Client{client: httpClient},
		ref:    ref,
		closed: make(chan struct{}),
	}
	subscription := &rta.Subscription{
		Custom: []byte(`{"ConnectionId":"` + oldConnectionID.String() + `"}`),
	}
	setSubscriptionCurrentForTest(subscription, 1, []byte(`{"ConnectionId":"`+newConnectionID.String()+`"}`))
	client := &Client{
		client:           httpClient,
		subscription:     subscription,
		subscriptionData: &subscriptionData{ConnectionID: oldConnectionID},
		sessions: map[string]*Session{
			ref.URL().String(): session,
		},
	}
	handler := &subscriptionHandler{Client: client}

	handler.HandleReconnect(nil)

	select {
	case request := <-requests:
		got := request.Members["me"].Properties.System.Connection
		if got != newConnectionID {
			t.Fatalf("connection ID = %v, want %v", got, newConnectionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session connection update")
	}
}

func TestSubscribeRefreshesCachedDataFromCurrentSubscriptionCustom(t *testing.T) {
	oldConnectionID := uuid.New()
	newConnectionID := uuid.New()
	subscription := &rta.Subscription{
		Custom: []byte(`{"ConnectionId":"` + oldConnectionID.String() + `"}`),
	}
	setSubscriptionCurrentForTest(subscription, 1, []byte(`{"ConnectionId":"`+newConnectionID.String()+`"}`))
	client := &Client{
		subscription:     subscription,
		subscriptionData: &subscriptionData{ConnectionID: oldConnectionID},
		sessions:         map[string]*Session{},
	}

	gotSubscription, gotData, err := client.subscribe(context.Background())
	if err != nil {
		t.Fatalf("subscribe returned error: %v", err)
	}
	if gotSubscription != subscription {
		t.Fatal("subscribe returned a different subscription")
	}
	if gotData.ConnectionID != newConnectionID {
		t.Fatalf("connection ID = %v, want %v", gotData.ConnectionID, newConnectionID)
	}
	if client.subscriptionData.ConnectionID != newConnectionID {
		t.Fatalf("cached connection ID = %v, want %v", client.subscriptionData.ConnectionID, newConnectionID)
	}
}

func setSubscriptionCurrentForTest(subscription *rta.Subscription, id uint32, custom []byte) {
	value := reflect.ValueOf(subscription).Elem()
	setUnexportedFieldForTest(value.FieldByName("currentID"), reflect.ValueOf(id))
	setUnexportedFieldForTest(value.FieldByName("currentCustom"), reflect.ValueOf(json.RawMessage(custom)))
	setUnexportedFieldForTest(value.FieldByName("currentSet"), reflect.ValueOf(true))
}

func setUnexportedFieldForTest(field, value reflect.Value) {
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(value)
}

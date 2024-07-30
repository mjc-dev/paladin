// Code generated by mockery v2.38.0. DO NOT EDIT.

package commsbusmocks

import (
	context "context"

	commsbus "github.com/kaleido-io/paladin/kata/internal/commsbus"

	mock "github.com/stretchr/testify/mock"
)

// Broker is an autogenerated mock type for the Broker type
type Broker struct {
	mock.Mock
}

// ListDestinations provides a mock function with given fields: ctx
func (_m *Broker) ListDestinations(ctx context.Context) ([]string, error) {
	ret := _m.Called(ctx)

	if len(ret) == 0 {
		panic("no return value specified for ListDestinations")
	}

	var r0 []string
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context) ([]string, error)); ok {
		return rf(ctx)
	}
	if rf, ok := ret.Get(0).(func(context.Context) []string); ok {
		r0 = rf(ctx)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]string)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context) error); ok {
		r1 = rf(ctx)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Listen provides a mock function with given fields: ctx, destination
func (_m *Broker) Listen(ctx context.Context, destination string) (commsbus.MessageHandler, error) {
	ret := _m.Called(ctx, destination)

	if len(ret) == 0 {
		panic("no return value specified for Listen")
	}

	var r0 commsbus.MessageHandler
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, string) (commsbus.MessageHandler, error)); ok {
		return rf(ctx, destination)
	}
	if rf, ok := ret.Get(0).(func(context.Context, string) commsbus.MessageHandler); ok {
		r0 = rf(ctx, destination)
	} else {
		r0 = ret.Get(0).(commsbus.MessageHandler)
	}

	if rf, ok := ret.Get(1).(func(context.Context, string) error); ok {
		r1 = rf(ctx, destination)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// PublishEvent provides a mock function with given fields: ctx, event
func (_m *Broker) PublishEvent(ctx context.Context, event commsbus.Event) error {
	ret := _m.Called(ctx, event)

	if len(ret) == 0 {
		panic("no return value specified for PublishEvent")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, commsbus.Event) error); ok {
		r0 = rf(ctx, event)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// SendMessage provides a mock function with given fields: ctx, message
func (_m *Broker) SendMessage(ctx context.Context, message commsbus.Message) error {
	ret := _m.Called(ctx, message)

	if len(ret) == 0 {
		panic("no return value specified for SendMessage")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, commsbus.Message) error); ok {
		r0 = rf(ctx, message)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// SubscribeToTopic provides a mock function with given fields: ctx, topic, destination
func (_m *Broker) SubscribeToTopic(ctx context.Context, topic string, destination string) error {
	ret := _m.Called(ctx, topic, destination)

	if len(ret) == 0 {
		panic("no return value specified for SubscribeToTopic")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, string, string) error); ok {
		r0 = rf(ctx, topic, destination)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Unlisten provides a mock function with given fields: ctx, destination
func (_m *Broker) Unlisten(ctx context.Context, destination string) error {
	ret := _m.Called(ctx, destination)

	if len(ret) == 0 {
		panic("no return value specified for Unlisten")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, string) error); ok {
		r0 = rf(ctx, destination)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// UnsubscribeFromTopic provides a mock function with given fields: ctx, topic, destination
func (_m *Broker) UnsubscribeFromTopic(ctx context.Context, topic string, destination string) error {
	ret := _m.Called(ctx, topic, destination)

	if len(ret) == 0 {
		panic("no return value specified for UnsubscribeFromTopic")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, string, string) error); ok {
		r0 = rf(ctx, topic, destination)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// NewBroker creates a new instance of Broker. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewBroker(t interface {
	mock.TestingT
	Cleanup(func())
}) *Broker {
	mock := &Broker{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
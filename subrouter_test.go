package mqttkit

import (
	"context"
	"reflect"
	"testing"
)

func TestSubContextOnionOrder(t *testing.T) {
	var called []string

	mw1 := func(sc *SubContext) {
		called = append(called, "mw1-pre")
		sc.Next()
		called = append(called, "mw1-post")
	}
	handler := func(sc *SubContext) {
		called = append(called, "handler")
	}

	sc := &SubContext{
		Context:  context.Background(),
		handlers: []SubHandler{mw1, handler},
	}
	sc.Next()

	want := []string{"mw1-pre", "handler", "mw1-post"}
	if !reflect.DeepEqual(called, want) {
		t.Fatalf("called=%v want=%v", called, want)
	}
}

func TestSubContextAbort(t *testing.T) {
	var called []string

	mw1 := func(sc *SubContext) {
		called = append(called, "mw1")
		sc.Abort()
	}
	handler := func(sc *SubContext) {
		called = append(called, "handler")
	}

	sc := &SubContext{
		Context:  context.Background(),
		handlers: []SubHandler{mw1, handler},
	}
	sc.Next()

	want := []string{"mw1"}
	if !reflect.DeepEqual(called, want) {
		t.Fatalf("called=%v want=%v", called, want)
	}
}

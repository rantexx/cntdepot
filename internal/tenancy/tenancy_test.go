package tenancy

import (
	"context"
	"testing"
)

func TestScopeRoundTrip(t *testing.T) {
	want := Scope{CompanyID: "c1", DepotID: "d1", UserID: "u1", Role: Gate}
	got, ok := From(With(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("got %+v %v, want %+v", got, ok, want)
	}
	if _, ok := From(context.Background()); ok {
		t.Fatal("empty context has a scope")
	}
}

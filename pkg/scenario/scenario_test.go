package scenario

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"infernosim/pkg/matcher"
)

func TestEngineTransitionsExplicitState(t *testing.T) {
	engine, err := New([]Config{{
		Name:         "checkout",
		InitialState: "anonymous",
		Steps: []Step{
			{
				Name:      "login",
				State:     "anonymous",
				NextState: "authenticated",
				Match: matcher.Rule{
					Methods:   []string{http.MethodPost},
					PathRegex: `^/login$`,
				},
				Response: Response{Status: 200, Body: `{"token":"ok"}`},
			},
			{
				Name:  "pay",
				State: "authenticated",
				Match: matcher.Rule{
					Methods:   []string{http.MethodPost},
					PathRegex: `^/pay$`,
				},
				Response: Response{Status: 201},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	pay := httptest.NewRequest(http.MethodPost, "http://dependency.test/pay", nil)
	if _, ok := engine.Match(pay, nil); ok {
		t.Fatal("pay must not match before login")
	}
	login := httptest.NewRequest(http.MethodPost, "http://dependency.test/login", nil)
	if result, ok := engine.Match(login, nil); !ok || result.Step != "login" {
		t.Fatalf("login did not match: %#v %t", result, ok)
	}
	if result, ok := engine.Match(pay, nil); !ok || result.Response.Status != 201 {
		t.Fatalf("pay did not match after login: %#v %t", result, ok)
	}
	engine.Reset()
	if _, ok := engine.Match(pay, nil); ok {
		t.Fatal("reset did not restore initial state")
	}
}

func TestEngineRejectsUnknownNextState(t *testing.T) {
	_, err := New([]Config{{
		Name: "bad", InitialState: "one",
		Steps: []Step{{
			State: "one", NextState: "missing",
			Response: Response{Status: 200},
		}},
	}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

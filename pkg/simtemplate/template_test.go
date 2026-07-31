package simtemplate

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func TestDeterministicRequestTemplates(t *testing.T) {
	engine, err := New(Config{Seed: "release"})
	if err != nil {
		t.Fatal(err)
	}
	var document any
	_ = json.Unmarshal([]byte(`{"customer":{"id":"cust-42"}}`), &document)
	data := Data{Request: Request{
		Method:  http.MethodPost,
		URL:     "https://orders.test/orders?region=eu",
		Path:    "/orders",
		Headers: http.Header{"X-Tenant": {"north"}},
		Query:   url.Values{"region": {"eu"}},
		JSON:    document,
		Body:    `{"customer":{"id":"cust-42"}}`,
	}}
	source := `{"id":"{{ uuid "order" }}","customer":"{{ jsonPath "$.customer.id" }}","tenant":"{{ header "X-Tenant" }}","region":"{{ query "region" }}","at":"{{ now }}"}`
	first, err := engine.Render("body", source, data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Render("body", source, data)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("template is not deterministic:\n%s\n%s", first, second)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(first), &output); err != nil {
		t.Fatal(err)
	}
	if output["customer"] != "cust-42" || output["tenant"] != "north" || output["region"] != "eu" {
		t.Fatalf("output=%#v", output)
	}
}

func TestTemplateOutputLimitAndSyntaxValidation(t *testing.T) {
	engine, _ := New(Config{MaxOutputBytes: 4})
	if _, err := engine.Render("body", "12345", Data{}); err == nil {
		t.Fatal("expected output limit failure")
	}
	if err := Validate(`{{ unknown "value" }}`); err == nil {
		t.Fatal("expected unknown function failure")
	}
}

func FuzzTemplateValidation(f *testing.F) {
	f.Add(`{{ uuid "id" }}`)
	f.Add(`{{ jsonPath "$.user.id" }}`)
	f.Fuzz(func(t *testing.T, source string) {
		_ = Validate(source)
	})
}

func BenchmarkRender(b *testing.B) {
	engine, _ := New(Config{Seed: "benchmark"})
	data := Data{Request: Request{Method: "GET", URL: "https://example.test/items", Path: "/items", Headers: make(http.Header), Query: make(url.Values)}}
	for i := 0; i < b.N; i++ {
		if _, err := engine.Render("body", `{"id":"{{ uuid "item" }}","at":"{{ now }}"}`, data); err != nil {
			b.Fatal(err)
		}
	}
}

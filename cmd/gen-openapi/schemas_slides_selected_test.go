package main

import "testing"

func TestSlideSelectedContract(t *testing.T) {
	catalog := slideSelectedCatalog()
	for _, route := range []string{"POST /api/v1/pages/{slug}/slides/requests", "POST /api/v1/pages/{slug}/slides/requests/{requestId}/cancel"} {
		schema, ok := catalog[route]
		if !ok || schema.Request["additionalProperties"] != false || schema.Response == nil {
			t.Fatal("strict selected request contract missing", route)
		}
	}
	components := slideSelectedComponents()
	for _, name := range []string{"SlideSelectedSummary", "SlideSelectedEvent", "SlideSelectedTextRange", "SlideSelectedRequests"} {
		if components[name] == nil {
			t.Fatal("missing selected DTO", name)
		}
	}
}

package main

import "testing"

func TestTemplateNameConflictsAreDocumented(t *testing.T) {
	operations := loadSpecOperations(t)
	for _, route := range []struct{ path, method string }{
		{"/api/v1/templates", "post"},
		{"/api/v1/templates/{templateId}", "patch"},
	} {
		if len(operations[route.path][route.method].Responses["409"]) == 0 {
			t.Errorf("%s %s omits duplicate-name409", route.method, route.path)
		}
	}
}

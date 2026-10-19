package main

import "testing"

func TestSlidesHumanReadHasNamedSchemaOneContract(t *testing.T) {
 document:=buildDocument([]route{routeFromKey("GET /api/v1/pages/{slug}/slides")})
 operation:=document["paths"].(map[string]any)["/api/v1/pages/{slug}/slides"].(map[string]any)["get"].(map[string]any)
 response:=operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
 if response["$ref"]!="#/components/schemas/SlidesRead" {t.Fatalf("read lacks named contract: %#v",response)}
 schema:=document["components"].(map[string]any)["schemas"].(map[string]any)["SlidesRead"].(map[string]any)
 if schema["additionalProperties"]!=false {t.Fatal("Slides read must be a closed projection")}
 props:=schema["properties"].(map[string]any)
 if len(props)!=6 {t.Fatalf("read projects %d fields, want6",len(props))}
 version:=props["schema_version"].(map[string]any)["enum"].([]int)
 if len(version)!=1 || version[0]!=1 {t.Fatal("public reader advertises unsupported schema")}
}

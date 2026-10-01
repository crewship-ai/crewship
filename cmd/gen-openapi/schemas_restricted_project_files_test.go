package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func projectFileWireFields(t *testing.T, file, name string) map[string]ast.Expr {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "../../internal/api/"+file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]ast.Expr{}
	found := false
	ast.Inspect(parsed, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != name {
			return true
		}
		typ, ok := spec.Type.(*ast.StructType)
		if !ok {
			t.Fatal("wire DTO is no longer a struct")
		}
		found = true
		for _, field := range typ.Fields.List {
			if field.Tag == nil {
				continue
			}
			tag, e := strconv.Unquote(field.Tag.Value)
			if e != nil {
				t.Fatal(e)
			}
			key := strings.Split(reflect.StructTag(tag).Get("json"), ",")[0]
			if key != "" && key != "-" {
				fields[key] = field.Type
			}
		}
		return false
	})
	if !found {
		t.Fatalf("wire DTO %s not found", name)
	}
	return fields
}

func TestRestrictedProjectFileSchemasMatchActualWireTypes(t *testing.T) {
	catalog := restrictedProjectFileSchemaCatalog()
	base := "/api/v1/workspaces/{workspaceId}/projects/{projectId}/files"
	version := catalog["POST "+base].Response
	properties := version["properties"].(map[string]any)
	required := map[string]bool{}
	for _, key := range version["required"].([]string) {
		required[key] = true
	}
	typ := reflect.TypeOf(access.ProjectFileVersion{})
	if len(properties) != typ.NumField() {
		t.Fatal("metadata schema has hidden or missing fields")
	}
	for i := 0; i < typ.NumField(); i++ {
		key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if properties[key] == nil || !required[key] {
			t.Fatalf("missing mandatory actual version field %s", key)
		}
	}
	request := catalog["POST "+base].Request
	requestProps := request["properties"].(map[string]any)
	fields := projectFileWireFields(t, "project_files.go", "ProjectFileWriteRequest")
	if len(fields) != len(requestProps) {
		t.Fatal("upload request fields differ from actual DTO")
	}
	for key := range fields {
		if requestProps[key] == nil {
			t.Fatalf("request dropped %s", key)
		}
	}
	bytes, ok := fields["content_base64"].(*ast.ArrayType)
	if !ok || bytes.Len != nil || bytes.Elt.(*ast.Ident).Name != "byte" || requestProps["content_base64"].(map[string]any)["format"] != "byte" {
		t.Fatal("upload no longer documents actual base64 byte field")
	}
	options := catalog["GET /api/v1/chats/{chatId}/project-input-options"].Response
	optionProps := options["properties"].(map[string]any)
	envelope := projectFileWireFields(t, "project_input_options.go", "ProjectInputOptionsResponse")
	if len(envelope) != len(optionProps) {
		t.Fatal("picker envelope differs from actual DTO")
	}
	for key := range envelope {
		if optionProps[key] == nil {
			t.Fatalf("picker dropped %s", key)
		}
	}
	item := optionProps["files"].(map[string]any)["items"].(map[string]any)
	if len(item["properties"].(map[string]any)) != typ.NumField()+1 || item["properties"].(map[string]any)["project_name"] == nil {
		t.Fatal("picker metadata widened or lost project name")
	}
}

func TestRestrictedProjectFileContractsKeepCASCapacityAndBinaryDownload(t *testing.T) {
	catalog := restrictedProjectFileSchemaCatalog()
	if len(catalog) != 5 {
		t.Fatalf("routes=%d", len(catalog))
	}
	base := "/api/v1/workspaces/{workspaceId}/projects/{projectId}/files"
	upload := catalog["POST "+base]
	if !upload.RequestRequired || upload.Request["additionalProperties"] != false || upload.SuccessStatuses[0] != "201" {
		t.Fatal("upload strictness/status lost")
	}
	props := upload.Request["properties"].(map[string]any)
	if props["content_base64"].(map[string]any)["maxLength"] != 1398104 || props["expected_revision"].(map[string]any)["minimum"] != 0 {
		t.Fatal("upload bounds/create revision lost")
	}
	retire := catalog["DELETE "+base+"/{fileId}"]
	if !retire.RequestRequired || retire.SuccessStatuses[0] != "204" || retire.Request["properties"].(map[string]any)["expected_revision"].(map[string]any)["minimum"] != 1 {
		t.Fatal("retirement CAS/no-body contract lost")
	}
	download := catalog["GET "+base+"/{versionId}/download"]
	if download.Response["format"] != "binary" || !reflect.DeepEqual(download.ResponseMedia, []string{"application/octet-stream"}) {
		t.Fatal("download claimed JSON")
	}
	for _, header := range []string{"Content-Length", "X-Content-SHA256"} {
		if download.SuccessHeaders[header] == nil {
			t.Fatalf("missing atomic download header %s", header)
		}
	}
	for key, contract := range catalog {
		if !reflect.DeepEqual(contract.ErrorMedia, []string{"application/json"}) || !reflect.DeepEqual(contract.ErrorResponse["required"], []string{"error"}) {
			t.Fatalf("%s changed error envelope", key)
		}
	}
}

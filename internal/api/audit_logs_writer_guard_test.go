package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// F1 guard: an authorization or permission-affecting change must append the
// chained journal on the writer transaction that makes the change. The legacy
// audit_logs table is unchained, and WriteAuditLog/auditFromRequest write it
// best effort after the change has committed. This test scans production Go
// under internal/ and cmd/ so a new legacy-only writer for an access event
// cannot land silently:
//
//   - A file with a direct `INSERT INTO audit_logs` must also append the chain
//     (appendAuthorizationAudit / appendSettingsAudit / AppendTx), or be listed
//     in legacyAuditInsertFiles with the reason it is not an access change.
//   - A best-effort WriteAuditLog/auditFromRequest call whose action names an
//     access concept fails unless that action is listed in
//     operationalAccessActions with its reason. A call with a non-literal
//     action must be listed in dynamicAuditActionSites.
//
// Adding an entry is a deliberate act that has to be justified in review.
var legacyAuditInsertFiles = map[string]string{
	"internal/api/internal_handler.go":               "WriteAuditLog itself: the best-effort writer this guard polices at its call sites",
	"internal/api/pages_private_metadata_read.go":    "read-access record (OWNER reading a private Page); fail-closed in the read transaction, no access changes",
	"internal/api/pages_private_audience.go":         "read-access record (OWNER reading a private Page); written on the read transaction, no access changes",
	"internal/chataudience/private_artifact_read.go": "read-access record (OWNER reading a private chat artifact); conditional insert re-checks authority, no access changes",
	"internal/api/agent_persona.go":                  "persona.suggest_pending proposal record read by the inbox; a pending suggestion changes no access",
}

// Actions that match accessActionPattern but do not change who may access what.
var operationalAccessActions = map[string]string{
	"credential_folder.create":          "a new folder is empty; its policy applies to no credential until one is filed, which is audited on the credential path",
	"credential_folder.delete":          "only an empty folder can be deleted; no credential's reveal policy or tier changes",
	"credential.security_level.clamped": "post-restore normalization record; the restore only ever tightens an undefined tier to the strictest one",
}

// Call sites (file:enclosing function) that pass a computed action. Each one
// is reviewed by hand: none of them can produce an access action.
var dynamicAuditActionSites = map[string]string{
	"internal/api/internal_handler.go:auditFromRequest": "the wrapper; its callers are checked",
	"internal/server/audit_adapter.go:RecordAudit":      "orchestrator agent-run records (agent.run.*): operational",
	"internal/api/backup.go:Restore":                    "backup.restore / backup.restore.dry_run",
	"internal/api/workspaces_logo.go:setLogoURL":        "workspace logo set/clear",
	"internal/api/agents_approve_hire.go:emit":          "agent.hire_approved / agent.hire_denied lifecycle",
}

var accessActionPattern = regexp.MustCompile(`(?i)role|member|grant|permission|polic|access|crew_link|capabilit|privilege|origin_binding|invit|share|acl|owner|credential_folder|security_level|reveal`)

var chainedAppendPattern = regexp.MustCompile(`appendAuthorizationAudit\(|appendSettingsAudit\(|\.AppendTx\(|journal\.AppendTx\(`)

var auditLogsInsertPattern = regexp.MustCompile(`(?is)insert\s+into\s+audit_logs\b`)

func TestAuditLogsWritersAreChainedForAccessChanges(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	seenDynamic := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if auditLogsInsertPattern.Match(src) {
				if _, listed := legacyAuditInsertFiles[rel]; !listed && !chainedAppendPattern.Match(src) {
					problems = append(problems, rel+": inserts into audit_logs without appending the chained journal; append it in the same transaction, or list the file with its reason")
				}
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					index := -1
					switch name := calleeName(call.Fun); name {
					case "WriteAuditLog":
						index = 3
					case "auditFromRequest":
						index = 2
					}
					if index < 0 || len(call.Args) <= index {
						return true
					}
					site := rel + ":" + fn.Name.Name
					lit, ok := call.Args[index].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						seenDynamic[site] = true
						if _, listed := dynamicAuditActionSites[site]; !listed {
							problems = append(problems, fset.Position(call.Pos()).String()+": best-effort audit with a computed action; list "+site+" with its reason")
						}
						return true
					}
					action, _ := strconv.Unquote(lit.Value)
					if _, listed := operationalAccessActions[action]; accessActionPattern.MatchString(action) && !listed {
						problems = append(problems, fset.Position(call.Pos()).String()+": access action "+strconv.Quote(action)+" is written best effort to audit_logs only; use appendSettingsAudit on the change's transaction")
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for site := range dynamicAuditActionSites {
		if !seenDynamic[site] {
			problems = append(problems, "stale dynamicAuditActionSites entry: "+site)
		}
	}
	for rel := range legacyAuditInsertFiles {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || !auditLogsInsertPattern.Match(src) {
			problems = append(problems, "stale legacyAuditInsertFiles entry: "+rel)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

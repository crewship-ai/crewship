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
// under internal/ and cmd/, call site by call site, so a new legacy-only
// writer for an access event cannot land silently, even in a file that is
// chained elsewhere:
//
//   - Every `INSERT INTO audit_logs` must sit in a function that itself
//     appends the chain (appendAuthorizationAudit / appendSettingsAudit /
//     appendAgentAudit / AppendTx), or that function must be listed in
//     legacyAuditInsertSites with the reason it is not an access change.
//
//   - A best-effort WriteAuditLog/auditFromRequest call fails when its action
//     names an access concept (accessActionPattern), or when its entity type
//     has privilege-bearing fields (privilegeFieldsByEntity) and its metadata
//     names one of them or cannot be read statically. Either is allowed only
//     with a per-site entry in reviewedAuditSites. A call with a computed
//     action or entity type must be listed in dynamicAuditActionSites.
//
//   - A function that builds a journal.Entry literal with an authorization
//     entry type (member.*, invitation.*, crew.member_changed, page.grant_*)
//     and sends it with Emit/EmitSync, without a chained append in the same
//     function, fails: Emit is buffered and outside the change's
//     transaction.
//
// Coverage note: the Emit rule sees entry types written as a journal.Entry
// literal. An entry whose Type is computed elsewhere is not seen; the one
// such best-effort path today is the GDPR erasure's page-grant revocation
// record (pages_erase_identity.go journalRevocations), which by design runs
// after the erasure commits. Generic audit.entity_* emits are judged through
// their WriteAuditLog/auditFromRequest call sites, and the ephemeral expiry
// sweeper's agent.expired record follows the expires_at the chained
// agent.hired/agent.rehired entry already fixed.
//
// Known limits. This is a source scan, not a proof; review still owns these:
//
//   - It judges a function, not a transaction. A chained append anywhere in
//     a function satisfies the insert and Emit rules even if it is on a
//     different transaction, or on a path that does not reach the insert.
//   - Field detection reads only map literals passed directly as metadata.
//     Metadata built elsewhere is flagged as unreadable for privileged
//     entities, but a privileged field under an unlisted entity type, or a
//     differently named field (a new column), is not seen.
//   - Entity types and authorization entry types are recognised by literal
//     name. A computed entity type is listed in dynamicAuditActionSites; a
//     computed journal Type is not seen at all.
//   - Writes that bypass audit_logs and the journal (a privilege change with
//     no audit at all) are invisible to it; the behaviour tests in
//     legacy_authorization_audit_test.go and agent_privilege_audit_test.go
//     cover the known paths.
//   - Only production .go files under internal/ and cmd/ are scanned.
//
// Adding an entry is a deliberate act that has to be justified in review.
var legacyAuditInsertSites = map[string]string{
	"internal/backup/dbdump.go:RestoreDumpTxHooks":                  "quarantine receipt for newly imported host rows before destination authority is admitted; copied approvals are stripped before INSERT, ignored existing rows are unchanged; atomic legacy recovery record, not a live grant",
	"internal/backup/host_hooks_recover.go:resetRecoveredHostHooks": "offline recovery normalization before server start; source signing keys or current migrations may be unavailable, so preserve source chain and atomically record legacy quarantine receipt without granting destination authority",

	"internal/api/internal_handler.go:WriteAuditLog":                             "the best-effort writer this guard polices at its call sites",
	"internal/api/pages_private_metadata_read.go:appendPrivateReadAudit":         "read-access record (OWNER reading a private Page); fail-closed in the read transaction, no access changes",
	"internal/api/pages_private_audience.go:recordPrivateOwnerAccess":            "read-access record (OWNER reading a private Page); written on the read transaction, no access changes",
	"internal/chataudience/private_artifact_read.go:CanReadArtifactConversation": "read-access record (OWNER reading a private chat artifact); conditional insert re-checks authority, no access changes",
	"internal/api/agent_persona.go:SuggestAgentPersona":                          "persona.suggest_pending proposal record read by the inbox; a pending suggestion changes no access",
}

// Entity types whose rows carry fields that decide what someone may reach.
// A best-effort audit of such an entity that records one of these fields is
// recording a permission change outside its transaction.
var privilegeFieldsByEntity = map[string][]string{
	"HOOK":              {"host_authorization", "before_authorization", "after_authorization"},
	"hook":              {"host_authorization", "before_authorization", "after_authorization"},
	"AGENT":             {"tool_profile", "mcp_config_json", "crew_id", "agent_role"},
	"CREDENTIAL_FOLDER": {"reveal_policy", "min_security_level", "default_security_level", "parent_id"},
}

// Best-effort sites (file:function:action) that the rules above flag and that
// have been reviewed as changing no access.
var reviewedAuditSites = map[string]string{
	"internal/api/credential_folders.go:Create:credential_folder.create":  "a new folder is empty; its policy applies to no credential until one is filed, which is audited on the credential path",
	"internal/api/credential_folders.go:Delete:credential_folder.delete":  "only an empty folder can be deleted; no credential's reveal policy or tier changes",
	"internal/api/backup.go:Restore:credential.security_level.clamped":    "post-restore normalization record; the restore only ever tightens an undefined tier to the strictest one",
	"internal/api/agents_approve_hire.go:ApproveHire:agent.hire_approved": "crew_id identifies the crew the hire already joined (chained as agent.hired); approval changes only its review status",
	"internal/api/agents_approve_hire.go:emit:<computed>":                 "agent.hire_approved / agent.hire_denied via the approvals queue; crew_id is context, membership was chained as agent.hired",
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

var chainedAppendPattern = regexp.MustCompile(`appendAuthorizationAudit\(|appendSettingsAudit\(|appendAgentAudit\(|\.AppendTx\(|journal\.AppendTx\(`)

var authorizationEntryTypePattern = regexp.MustCompile(`^Entry(Member|Invitation|CrewMember|PageGrant)`)

var bufferedEmitPattern = regexp.MustCompile(`\.Emit(Sync)?\(`)

// Sites (file:function) that emit an authorization entry type without the
// chain, each reviewed. Empty today.
var unchainedAuthorizationEmitSites = map[string]string{}

var auditLogsInsertPattern = regexp.MustCompile(`(?is)insert\s+into\s+audit_logs\b`)

func TestAuditLogsWritersAreChainedForAccessChanges(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	seen := map[string]bool{}
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
			fileProblems, err := auditGuardFile(path, rel, src, seen)
			problems = append(problems, fileProblems...)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for site := range dynamicAuditActionSites {
		if !seen["dynamic:"+site] {
			problems = append(problems, "stale dynamicAuditActionSites entry: "+site)
		}
	}
	for site := range legacyAuditInsertSites {
		if !seen["insert:"+site] {
			problems = append(problems, "stale legacyAuditInsertSites entry: "+site)
		}
	}
	for site := range unchainedAuthorizationEmitSites {
		if !seen["emit:"+site] {
			problems = append(problems, "stale unchainedAuthorizationEmitSites entry: "+site)
		}
	}
	for site := range reviewedAuditSites {
		if !seen["reviewed:"+site] {
			problems = append(problems, "stale reviewedAuditSites entry: "+site)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// auditGuardFile applies the guard's rules to one source file. seen collects
// the allowlist entries the file used, so stale ones can be reported.
func auditGuardFile(path, rel string, src []byte, seen map[string]bool) ([]string, error) {
	var problems []string
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, err
	}
	// An insert outside any function (a package-level query) has no
	// transaction of its own to check: it is always a problem.
	for _, decl := range file.Decls {
		if _, ok := decl.(*ast.FuncDecl); ok {
			continue
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && auditLogsInsertPattern.MatchString(lit.Value) {
				problems = append(problems, fset.Position(lit.Pos()).String()+": package-level audit_logs insert; write it inside the function that appends the chain")
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fnSite := rel + ":" + fn.Name.Name
		body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
		if authorizationEntryLiteral(fn.Body) && bufferedEmitPattern.MatchString(body) && !chainedAppendPattern.MatchString(body) {
			seen["emit:"+fnSite] = true
			if _, listed := unchainedAuthorizationEmitSites[fnSite]; !listed {
				problems = append(problems, fset.Position(fn.Pos()).String()+": "+fnSite+" emits an authorization journal entry outside the change's transaction; append it with AppendTx on that transaction")
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && auditLogsInsertPattern.MatchString(lit.Value) {
				seen["insert:"+fnSite] = true
				if _, listed := legacyAuditInsertSites[fnSite]; !listed && !chainedAppendPattern.MatchString(body) {
					problems = append(problems, fset.Position(lit.Pos()).String()+": inserts into audit_logs in "+fnSite+" without appending the chained journal there; append it in the same transaction, or list the site with its reason")
				}
				return true
			}
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
			if index < 0 || len(call.Args) <= index+2 {
				return true
			}
			pos := fset.Position(call.Pos()).String()
			action, actionOK := stringLiteral(call.Args[index])
			entity, entityOK := stringLiteral(call.Args[index+1])
			if !actionOK || !entityOK {
				seen["dynamic:"+fnSite] = true
				if _, listed := dynamicAuditActionSites[fnSite]; !listed {
					problems = append(problems, pos+": best-effort audit with a computed action or entity; list "+fnSite+" with its reason")
				}
			}
			if !actionOK {
				action = "<computed>"
			}
			site := fnSite + ":" + action
			var reasons []string
			if actionOK && accessActionPattern.MatchString(action) {
				reasons = append(reasons, "access action "+strconv.Quote(action))
			}
			if fields, ok := privilegeFieldsByEntity[entity]; ok {
				metadata := call.Args[len(call.Args)-1]
				if keys, static := literalMapKeys(metadata); !static {
					reasons = append(reasons, entity+" audit with metadata that cannot be read statically")
				} else {
					for _, f := range fields {
						if keys[f] {
							reasons = append(reasons, entity+" privilege field "+strconv.Quote(f))
						}
					}
				}
			}
			if len(reasons) == 0 {
				return true
			}
			seen["reviewed:"+site] = true
			if _, listed := reviewedAuditSites[site]; !listed {
				problems = append(problems, pos+": "+strings.Join(reasons, ", ")+" written best effort to audit_logs only ("+site+"); append it on the change's transaction")
			}
			return true
		})
	}
	return problems, nil
}

// authorizationEntryLiteral reports whether body builds a journal.Entry whose
// Type is an authorization entry type constant.
func authorizationEntryLiteral(body ast.Node) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || found {
			return !found
		}
		if sel, ok := lit.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Entry" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Type" && authorizationEntryTypePattern.MatchString(calleeName(kv.Value)) {
				found = true
			}
		}
		return !found
	})
	return found
}

func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// literalMapKeys returns the string keys of a map composite literal, or
// static=false when the metadata is anything else (a variable, a call) whose
// keys the guard cannot see. nil metadata records nothing.
func literalMapKeys(e ast.Expr) (keys map[string]bool, static bool) {
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return map[string]bool{}, true
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	if _, isMap := lit.Type.(*ast.MapType); !isMap {
		return nil, false
	}
	keys = map[string]bool{}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil, false
		}
		k, ok := stringLiteral(kv.Key)
		if !ok {
			return nil, false
		}
		keys[k] = true
	}
	return keys, true
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

// The guard judges each call site on its own: a file that appends the chain in
// one function must not hide an unchained insert in another, and an AGENT
// audit is judged by the fields it records, not only its action name.
func TestAuditGuardJudgesEachCallSite(t *testing.T) {
	const header = "package x\n\n"
	tests := []struct {
		name, src string
		problems  int
	}{
		{"chained insert", `func a(tx T) { tx.Exec("INSERT INTO audit_logs(id) VALUES(?)"); appendAgentAudit(tx) }`, 0},
		{"second unchained insert in a chained file", `func a(tx T) { tx.Exec("INSERT INTO audit_logs(id) VALUES(?)"); appendAgentAudit(tx) }
func b(db D) { db.Exec("INSERT INTO audit_logs(id) VALUES(?)") }`, 1},
		{"package-level insert", "const q = `INSERT INTO audit_logs(id) VALUES(?)`", 1},
		{"host approval field under a neutral action", `func a() { WriteAuditLog(c, db, j, "update", "hook", id, u, w, map[string]any{"after_authorization": p}) }`, 1},
		{"non-host hook operational metadata", `func a() { WriteAuditLog(c, db, j, "hook.update", "hook", id, u, w, map[string]any{"handler_kind": "http"}) }`, 0},
		{"agent rename", `func a() { WriteAuditLog(c, db, j, "update", "AGENT", id, u, w, map[string]any{"name": n}) }`, 0},
		{"agent privilege field under a neutral action", `func a() { WriteAuditLog(c, db, j, "update", "AGENT", id, u, w, map[string]any{"tool_profile": p}) }`, 1},
		{"agent metadata the guard cannot read", `func a() { WriteAuditLog(c, db, j, "update", "AGENT", id, u, w, changes) }`, 1},
		{"access action on another entity", `func a() { auditFromRequest(r, db, "member.role.update", "WORKSPACE", id, nil) }`, 1},
		{"computed entity", `func a() { auditFromRequest(r, db, "update", kind, id, nil) }`, 1},
		{"buffered emit of a membership change", `func a() { j.Emit(c, journal.Entry{Type: journal.EntryMemberRoleChanged}) }`, 1},
		{"membership entry appended on the transaction", `func a() { e := journal.Entry{Type: journal.EntryMemberRoleChanged}; journal.AppendTx(c, tx, e); j.Emit(c, other) }`, 0},
		{"buffered emit of an operational entry", `func a() { j.Emit(c, journal.Entry{Type: journal.EntryAgentMentioned}) }`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := auditGuardFile("x.go", "internal/x/x.go", []byte(header+tc.src), map[string]bool{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.problems {
				t.Fatalf("problems = %d, want %d: %v", len(got), tc.problems, got)
			}
		})
	}
}

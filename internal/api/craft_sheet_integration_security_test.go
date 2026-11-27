package api

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// X-Craft-Chat names a chat that is not the caller's: another member's chat, the caller's chat in
// another workspace, a missing one. Every one is the same 404 chat_not_found, and nothing is
// created: no version, no chat reference, no agent file, no storage reservation left behind.
func TestSheetITInitializeWithAForeignChatCreatesNothing(t *testing.T) {
	f, h, ctx := craftNativeHTTPFixture(t)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	mustExec(t, f.db, `INSERT INTO users(id,email) VALUES('sit-other-user','sit-other@example.invalid')`)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('sit-other-member',?,'sit-other-user','MEMBER')`, f.wsID)
	mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('sit-ws-2','Second','sit-second')`)
	mustExec(t, f.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('sit-agent',?,'A','sit-agent')`, f.wsID)
	mustExec(t, f.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('sit-agent-2','sit-ws-2','B','sit-agent-2')`)
	mustExec(t, f.db, `INSERT INTO chats(id,agent_id,workspace_id,created_by) VALUES('sit-chat-member','sit-agent',?,'sit-other-user')`, f.wsID)
	mustExec(t, f.db, `INSERT INTO chats(id,agent_id,workspace_id,created_by) VALUES('sit-chat-elsewhere','sit-agent-2','sit-ws-2',?)`, f.userID)
	for _, chat := range []string{"sit-chat-member", "sit-chat-elsewhere", "sit-chat-missing", ""} {
		page := "sit-wb-" + strings.TrimPrefix(chat, "sit-chat-")
		f.seedPage(t, page, page, page, f.userID, "")
		req := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/initialize", bytes.NewReader(sitWorkbook(sitInitialL(), sitInitialOther()))).WithContext(ctx)
		req.SetPathValue("slug", "@"+page)
		req.Header.Set("Content-Type", mooring.WorkbookMediaTypeXLSM)
		req.Header.Set("X-Craft-File-Name", page+".xlsm")
		req.Header.Set("X-Craft-Chat", chat)
		w := httptest.NewRecorder()
		d.initialize(w, req)
		var versions, refs, files, reservations int
		_ = f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=?`, page).Scan(&versions)
		_ = f.db.QueryRow(`SELECT count(*) FROM artifact_chat_refs WHERE page_id=?`, page).Scan(&refs)
		_ = f.db.QueryRow(`SELECT count(*) FROM agent_files WHERE page_id=?`, page).Scan(&files)
		_ = f.db.QueryRow(`SELECT count(*) FROM artifact_storage_reservations`).Scan(&reservations)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"chat_not_found"`) || versions+refs+files+reservations != 0 {
			t.Errorf("X-Craft-Chat %q: %d %s; versions %d refs %d agent_files %d reservations %d", chat, w.Code, w.Body, versions, refs, files, reservations)
		}
	}
}

// sitAgentFileCall is one message without a chip in the scenario's chat, the Sheet kept as the
// agent's file, and the agent's read, lease and apply. It reports whether a run started at all.
func (s *sitScenario) sitAgentFileCall(content string) (read, acquire, apply string, readRefused, acquireRefused, applyRefused, ran bool) {
	s.t.Helper()
	if err := s.trySend(content); !errors.Is(err, errScenarioModel) {
		s.t.Logf("no run for %q: %v", content, err)
		return "", "", "", true, true, true, false
	}
	call := s.agent()
	read, readRefused = s.toolCall(call, "craft_sheet_read", map[string]any{"query": map[string]any{"command": "cell.get", "params": map[string]any{"sheet": "L", "cell": "B1"}}})
	acquire, acquireRefused = s.toolCall(call, "craft_sheet_acquire", map[string]any{})
	apply, applyRefused = s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": "1", "base_version": "1", "request_id": "sit-unattached", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "x"}))})
	return read, acquire, apply, readRefused, acquireRefused, applyRefused, true
}

func (s *sitScenario) sitNothingWritten() {
	s.t.Helper()
	var leases int
	if err := s.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_edit_leases`).Scan(&leases); err != nil || leases != 0 || s.head() != 1 || len(s.opsRows()) != 0 {
		s.t.Fatalf("an agent file alone wrote: %d leases (%v), head %d, ops %d", leases, err, s.head(), len(s.opsRows()))
	}
}

// agent_files (the agent-files rule). In the person's own private chat with the agent, a Sheet kept as the
// agent's file and still open to the person is READ without a chip, and every such read is audited
// with source agent_files; a lease and a change still need the chip of that message (403
// not_attached, told plainly). agent_files alone grants nothing: the link removed, a Sheet the
// person may no longer open, or a group chat.
func TestSheetITAgentFileAloneGrantsNothing(t *testing.T) {
	keep := func(s *sitScenario) {
		mustExec(t, s.f.rig.db, `INSERT INTO agent_files(agent_id,page_id,workspace_id,added_by,added_at) VALUES(?,'sheet-own',?,?,'2026-11-26T00:00:00Z')`, s.f.a.AgentID, s.f.a.WorkspaceID, s.f.a.HumanUserID)
	}
	t.Run("own private chat reads, never writes", func(t *testing.T) {
		s := newSheetITScenario(t)
		keep(s)
		read, acquire, apply, readRefused, acquireRefused, applyRefused, ran := s.sitAgentFileCall("What is in my sheet?")
		if !ran || readRefused || !strings.Contains(read, `"value":"Status"`) {
			t.Errorf("read through agent_files in the person's own private chat: ran=%v refused=%v %.200s", ran, readRefused, read)
		}
		if !acquireRefused || !strings.Contains(acquire, "not_attached") {
			t.Errorf("lease through agent_files alone: refused=%v %.200s", acquireRefused, acquire)
		}
		if !applyRefused || !strings.Contains(apply, "not_attached") || !strings.Contains(strings.ToLower(apply), "attach") {
			t.Errorf("apply through agent_files alone must be 403 not_attached, told plainly: refused=%v %.200s", applyRefused, apply)
		}
		s.sitNothingWritten()
		var audited int
		_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE json_extract(metadata,'$.source')='agent_files'`).Scan(&audited)
		if !readRefused && audited == 0 {
			t.Errorf("a read through agent_files is not audited with source agent_files")
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(*sitScenario)
	}{
		{"link removed", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `UPDATE agent_files SET removed_at='2026-11-26T01:00:00Z',removed_by=? WHERE page_id='sheet-own'`, s.f.a.HumanUserID)
		}},
		{"the person may no longer open the Sheet", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `INSERT INTO users(id,email) VALUES('sit-stranger','sit-stranger@example.invalid')`)
			mustExec(t, s.f.rig.db, `UPDATE pages SET owner_user_id='sit-stranger',private_audience=1 WHERE id='sheet-own'`)
		}},
		{"group chat", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `INSERT INTO users(id,email) VALUES('sit-member','sit-member@example.invalid')`)
			mustExec(t, s.f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, s.f.a.ChatID)
			mustExec(t, s.f.rig.db, `INSERT INTO chat_participants(chat_id,user_id) VALUES(?,?),(?,'sit-member')`, s.f.a.ChatID, s.f.a.HumanUserID, s.f.a.ChatID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			keep(s)
			tc.setup(s)
			read, acquire, apply, readRefused, acquireRefused, applyRefused, _ := s.sitAgentFileCall("What is in my sheet?")
			for name, got := range map[string]struct {
				text    string
				refused bool
			}{"read": {read, readRefused}, "acquire": {acquire, acquireRefused}, "apply": {apply, applyRefused}} {
				if !got.refused || strings.Contains(got.text, "Status") {
					t.Errorf("%s with only an agent file (%s): refused=%v %.200s", name, tc.name, got.refused, got.text)
				}
			}
			if opens, _ := s.wb.count(); opens != 0 {
				t.Errorf("the engine opened the Sheet %d times", opens)
			}
			s.sitNothingWritten()
		})
	}
}

// sitForgedPart is a zip entry whose central directory declares size bytes unpacked while it carries
// only compressed bytes: the archive check reads the declaration, never inflates it.
type sitForgedPart struct {
	name             string
	size, compressed uint64
}

// sitForgedWorkbook is a valid xlsx shell (content types, workbook, one sheet) plus forged parts and
// count-1 empty filler parts.
func sitForgedWorkbook(t *testing.T, forged []sitForgedPart, fillers int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	write("[Content_Types].xml", `<Types><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`)
	write("xl/workbook.xml", `<workbook/>`)
	write("xl/worksheets/sheet1.xml", `<worksheet><sheetData/></worksheet>`)
	for i := range fillers {
		write(fmt.Sprintf("xl/media/f%05d.bin", i), "")
	}
	r := rand.New(rand.NewPCG(1, 2))
	for _, p := range forged {
		raw := make([]byte, p.compressed)
		for i := range raw {
			raw[i] = byte(r.Uint32())
		}
		w, err := zw.CreateRaw(&zip.FileHeader{Name: p.name, Method: zip.Deflate, CompressedSize64: p.compressed, UncompressedSize64: p.size, CRC32: crc32.ChecksumIEEE(raw)})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(raw)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The archive limits of a stored workbook, each at its edge: 10 000 parts, 512 MiB unpacked in all
// and per part, a ratio of 200 per part over 1 MiB and for the whole archive over 64 MiB.
func TestSheetITWorkbookArchiveLimitsAtTheirEdges(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name    string
		forged  []sitForgedPart
		fillers int
		refused bool
	}{
		{"10 000 parts", nil, 10_000 - 3, false},
		{"10 001 parts", nil, 10_001 - 3, true},
		{"one part of 511 MiB at ratio 128", []sitForgedPart{{"xl/media/big.bin", 511 * mib, 4 * mib}}, 0, false},
		{"one part over 512 MiB", []sitForgedPart{{"xl/media/big.bin", 512*mib + 1, 4 * mib}}, 0, true},
		{"three parts, 513 MiB in all", []sitForgedPart{{"xl/media/a.bin", 171 * mib, mib}, {"xl/media/b.bin", 171 * mib, mib}, {"xl/media/c.bin", 171 * mib, mib}}, 0, true},
		{"a part over 1 MiB at ratio 200", []sitForgedPart{{"xl/media/r.bin", 200 * 10_000, 10_000}}, 0, false},
		{"a part over 1 MiB at ratio 201", []sitForgedPart{{"xl/media/r.bin", 201 * 10_000, 10_000}}, 0, true},
		{"many 1 MiB parts at ratio 256, 100 MiB in all", sitMany(100, mib, 4096), 0, true},
		{"many 1 MiB parts at ratio 256, 60 MiB in all", sitMany(60, mib, 4096), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := craftWorkbookValidate(t.Context(), sitForgedWorkbook(t, tc.forged, tc.fillers))
			if tc.refused != (err != nil) || err != nil && !errors.Is(err, craftsession.ErrTooLarge) {
				t.Fatalf("refused=%v want %v: %v", err != nil, tc.refused, err)
			}
		})
	}
}

func sitMany(n int, size, compressed uint64) []sitForgedPart {
	out := make([]sitForgedPart, n)
	for i := range out {
		out[i] = sitForgedPart{fmt.Sprintf("xl/media/m%03d.bin", i), size, compressed}
	}
	return out
}

// The refusals the save of a workbook Sheet answers before or instead of storing anything, each
// with its code, and the version unchanged.
func TestSheetITWorkbookSaveRefusesBombsEncryptedAndOversize(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	encrypted := append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), bytes.Repeat([]byte{0}, 512)...)
	encrypted = append(encrypted, []byte("E\x00n\x00c\x00r\x00y\x00p\x00t\x00e\x00d\x00P\x00a\x00c\x00k\x00a\x00g\x00e\x00")...)
	legacy := append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), bytes.Repeat([]byte{0}, 1024)...)
	// A zip whose entry carries the encryption flag (ZipCrypto): never opened.
	var flagged bytes.Buffer
	zw := zip.NewWriter(&flagged)
	for _, name := range []string{"[Content_Types].xml", "xl/workbook.xml", "xl/worksheets/sheet1.xml"} {
		body := `<workbook/>`
		if name == "[Content_Types].xml" {
			body = `<Types><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Store}
		if name == "xl/worksheets/sheet1.xml" {
			hdr.Flags |= 0x1
		}
		w, _ := zw.CreateHeader(hdr)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	bomb := sitForgedWorkbook(t, sitMany(100, 1<<20, 4096), 0)
	for _, tc := range []struct {
		name   string
		body   []byte
		status int
		code   string
	}{
		{"over the JSON editor limit", make([]byte, 32<<20+1), 413, "too_large"},
		{"password protected", encrypted, 415, "encrypted"},
		{"legacy xls", legacy, 415, "unsupported"},
		{"encrypted zip entry", flagged.Bytes(), 415, "not_workbook"},
		{"zip bomb by ratio", bomb, 413, "unpacked_too_large"},
	} {
		w := rig.save(tc.body, "1", workbookHash(tc.body))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
	var versions int
	if err := rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='native-page'`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("%d versions after refusals: %v", versions, err)
	}
}

// sitLargeWorkbook is a valid xlsm of exactly size bytes: the synthetic workbook plus one stored
// (uncompressed) random part that fills it up.
func sitLargeWorkbook(t *testing.T, size int) []byte {
	t.Helper()
	pad := size - len(sitWorkbook(sitInitialL(), sitInitialOther(), sitPart{"xl/media/pad.bin", ""}))
	for range 4 {
		r := rand.New(rand.NewPCG(3, 4))
		raw := make([]byte, pad)
		for i := range raw {
			raw[i] = byte(r.Uint32())
		}
		parts := sitBaseParts()
		parts = append(parts, sitPart{"xl/worksheets/sheet1.xml", sitSheetXML(sitInitialL())}, sitPart{"xl/worksheets/sheet2.xml", sitSheetXML(sitInitialOther())})
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, p := range parts {
			w, _ := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate})
			_, _ = w.Write([]byte(p.body))
		}
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: "xl/media/pad.bin", Method: zip.Store})
		_, _ = w.Write(raw)
		_ = zw.Close()
		if buf.Len() == size {
			return buf.Bytes()
		}
		pad += size - buf.Len()
	}
	t.Fatalf("could not build a workbook of %d bytes", size)
	return nil
}

// 32 MiB is the workbook limit of the JSON editor. A file just under it is saved; the storage
// reservation adds 256 KiB of overhead and must not refuse it as an invalid manifest.
func TestSheetITWorkbookJustUnderTheLimitIsSaved(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	body := sitLargeWorkbook(t, (32<<20)-(64<<10))
	w := rig.sitSaveAttempt(body, "1", "sit-large")
	if w.Code != 200 || rig.sitHead(t) != 2 {
		t.Fatalf("BUG a %d-byte workbook (under the JSON editor limit) is refused: %d %s (saveWorkbook reserves ContentLength+256 KiB, ReserveUpload allows at most %d)", len(body), w.Code, w.Body, artifacts.MaxStorageUploadBytes)
	}
}

// Every agent change of a workbook Sheet reserves its candidate limit plus 256 KiB of storage before
// the engine runs. That reservation must be one the store admits, or no agent change of a workbook
// Sheet ever reaches the engine.
func TestSheetITAgentChangeReservationFitsTheStorageLimit(t *testing.T) {
	store := mooring.NewNativeStore(nil, mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64)})
	if reserved := int64(nativeAgentCandidateLimit(store)) + 256<<10; reserved > artifacts.MaxStorageUploadBytes {
		t.Fatalf("BUG applyTool reserves %d bytes for a workbook Sheet, ReserveUpload admits at most %d: every agent apply of a workbook Sheet is refused with ErrVersionInvalid (422 engine_failed) before the engine runs (craft_native_agent_tool.go applyTool)", reserved, artifacts.MaxStorageUploadBytes)
	}
}

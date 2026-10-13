package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func boardRaster(t *testing.T, jpg bool) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	im.Set(1, 1, color.NRGBA{R: 220, A: 255})
	var out bytes.Buffer
	var err error
	if jpg {
		err = jpeg.Encode(&out, im, nil)
	} else {
		err = png.Encode(&out, im)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func boardFileUpload(t *testing.T, h *PageHandler, ws, user, slug, mutation string, base int64, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "POST", fmt.Sprintf("/?base_seq=%d&mutation_id=%s", base, mutation), ws, user, "OWNER", "")
	r.SetPathValue("slug", slug)
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.Header.Set("Content-Type", "text/html")
	rr := httptest.NewRecorder()
	h.UploadBoardFile(rr, r)
	return rr
}
func boardFileRead(t *testing.T, h *PageHandler, ws, user, slug, file string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "GET", "/", ws, user, "MEMBER", "")
	r.SetPathValue("slug", slug)
	r.SetPathValue("fileId", file)
	rr := httptest.NewRecorder()
	h.GetBoardFile(rr, r)
	return rr
}
func boardFileFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, _, _, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	return h, ws, user, page
}
func boardFileID(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var response struct {
		ID string `json:"file_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || response.ID == "" {
		t.Fatalf("file response %s %v", rr.Body.String(), err)
	}
	return response.ID
}

func TestBoardRasterCanonicalizationRejectsHostileImages(t *testing.T) {
	for _, jpegInput := range []bool{false, true} {
		raw := append(boardRaster(t, jpegInput), []byte("<script>secret-polyglot</script>")...)
		clean, mime, w, h, err := canonicalBoardImage(raw)
		if err != nil || w != 4 || h != 3 || bytes.Contains(clean, []byte("secret-polyglot")) {
			t.Fatalf("canonicalization %s %d %d %v", mime, w, h, err)
		}
	}
	huge := append([]byte(nil), boardRaster(t, false)...)
	binary.BigEndian.PutUint32(huge[16:20], 2049)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for _, raw := range [][]byte{[]byte("<svg onload='alert(1)'/>"), []byte("<html>payload</html>"), []byte("GIF89a"), huge, boardRaster(t, false)[:25], bytes.Repeat([]byte("x"), maxBoardFileInput+1)} {
		if _, _, _, _, err := canonicalBoardImage(raw); err == nil {
			t.Fatal("hostile/truncated/oversize image accepted")
		}
	}
}
func TestBoardFilesACLIdempotenceQuotaAndLifetimeGC(t *testing.T) {
	h, ws, user, page := boardFileFixture(t)
	raw := boardRaster(t, false)
	rr := boardFileUpload(t, h, ws, user, "report", "image", 0, raw)
	artifactExpect(t, rr, 201)
	file := boardFileID(t, rr)
	artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "image", 0, raw), 200)
	artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "image", 0, boardRaster(t, true)), 409)
	pagesSeedUser(t, h, ws, "image-reader", "image-reader@example.com", "MEMBER")
	artifactExpect(t, boardFileRead(t, h, ws, "image-reader", "report", file), 404)
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"image-reader@example.com","level":"read"}`)
	read := boardFileRead(t, h, ws, "image-reader", "report", file)
	artifactExpect(t, read, 200)
	if read.Header().Get("Content-Type") != "image/png" || read.Header().Get("X-Content-Type-Options") != "nosniff" || read.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe image headers")
	}
	if _, err := png.Decode(bytes.NewReader(read.Body.Bytes())); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_id='image-reader'`)
	artifactExpect(t, boardFileRead(t, h, ws, "image-reader", "report", file), 404)
	r := pagesGrantRequest(t, "POST", "/?base_seq=1&mutation_id=agent", ws, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.Header.Set(actingAgentSlugHeader, "agent")
	r.Body = io.NopCloser(bytes.NewReader(raw))
	denied := httptest.NewRecorder()
	h.UploadBoardFile(denied, r)
	artifactExpect(t, denied, 401)
	// Unused imports retain bytes; the rejected conflicting JPEG is an orphan.
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 1 {
		t.Fatalf("rejected-upload orphan was not reclaimed: %d %v", n, err)
	}
	artifactExpect(t, boardFileRead(t, h, ws, user, "report", file), 200)
	for i := 0; i < 99; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_board_files(id,board_page_id,sha256,content_type,width,height,byte_size,author_user_id,created_at) SELECT ?,board_page_id,sha256,content_type,width,height,byte_size,author_user_id,created_at FROM page_board_files WHERE id=?`, fmt.Sprint("quota-", i), file)
	}
	artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "new-quota", 1, raw), 409)
	artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "image", 0, raw), 200) // retry precedes quota
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, page)
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 1 {
		t.Fatalf("deleted board file retained: %d %v", n, err)
	}
}
func TestBoardFilesSceneReferenceAndBackupFork(t *testing.T) {
	h, ws, user, page := boardFileFixture(t)
	raw := boardRaster(t, false)
	rr := boardFileUpload(t, h, ws, user, "report", "image", 0, raw)
	artifactExpect(t, rr, 201)
	file := boardFileID(t, rr)
	var element map[string]any
	if err := json.Unmarshal(boardAPIElement(t), &element); err != nil {
		t.Fatal(err)
	}
	element["id"] = "board-image"
	element["type"] = "image"
	element["fileId"] = file
	element["status"] = "saved"
	element["scale"] = []int{1, 1}
	element["crop"] = nil
	drawing, _ := json.Marshal(element)
	body := fmt.Sprintf(`{"mutation_id":"place-image","base_seq":1,"upserts":[{"expected_rev":0,"element":%s}]}`, drawing)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", body), 201)
	element["fileId"] = "foreign-file"
	drawing, _ = json.Marshal(element)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", fmt.Sprintf(`{"mutation_id":"foreign","base_seq":2,"upserts":[{"expected_rev":2,"element":%s}]}`, drawing)), 400)
	const pass = "board-raster-backup-password"
	created, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []string{"", "board-images-fork"} {
		t.Run(fork, func(t *testing.T) {
			target := setupTestDB(t)
			root := storageDir(t)
			if _, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root}); err != nil {
				t.Fatal(err)
			}
			var newFile, newPage, newWS, sha, stored, receipt string
			if err := target.QueryRow(`SELECT f.id,f.board_page_id,p.workspace_id,f.sha256,e.element_json,m.result_file_id FROM page_board_files f JOIN pages p ON p.id=f.board_page_id JOIN page_board_elements e ON e.board_page_id=f.board_page_id JOIN page_board_mutations m ON m.result_file_id=f.id`).Scan(&newFile, &newPage, &newWS, &sha, &stored, &receipt); err != nil {
				t.Fatal(err)
			}
			var restored map[string]any
			if err := json.Unmarshal([]byte(stored), &restored); err != nil {
				t.Fatal(err)
			}
			if restored["fileId"] != newFile || receipt != newFile || (fork != "" && (newFile == file || newPage == page)) {
				t.Fatal("fork image/receipt references did not follow file")
			}
			data, err := readAttachmentBlob(root, newWS, sha)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = png.Decode(bytes.NewReader(data)); err != nil {
				t.Fatal("restored image missing/corrupt", err)
			}
		})
	}
	// Deleting an element keeps the file for explicit undo/restore.
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", `{"mutation_id":"delete-image","base_seq":2,"deletes":[{"id":"board-image","expected_rev":2}]}`), 201)
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 0 {
		t.Fatalf("image tombstone lost undo bytes: %d %v", n, err)
	}
}

func TestBoardFilesByteAndWorkspaceQuotas(t *testing.T) {
	for _, workspaceLimit := range []bool{false, true} {
		t.Run(fmt.Sprint(workspaceLimit), func(t *testing.T) {
			h, ws, user, page := boardFileFixture(t)
			raw := boardRaster(t, false)
			uploaded := boardFileUpload(t, h, ws, user, "report", "first", 0, raw)
			artifactExpect(t, uploaded, 201)
			file := boardFileID(t, uploaded)
			if !workspaceLimit {
				// Fill the logical quota without allocating dozens of decoded pixel buffers.
				for i := 0; i < 7; i++ {
					size := 8 << 20
					if i == 6 {
						size = (2 << 20) - uploadedImageSize(t, h, file)
					}
					execOrFatal(t, h.db, `INSERT INTO page_board_files(id,board_page_id,sha256,content_type,width,height,byte_size,created_at) SELECT ?,board_page_id,sha256,content_type,width,height,?,created_at FROM page_board_files WHERE id=?`, fmt.Sprint("byte-quota-", i), size, file)
				}
			} else {
				for i := 0; i < 10; i++ {
					board := fmt.Sprint("workspace-quota-", i)
					execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, board, ws, board, board, user)
					execOrFatal(t, h.db, `INSERT INTO page_boards(page_id,seq,format) SELECT ?,0,format FROM page_boards WHERE page_id=?`, board, page)
					count := 100
					if i == 9 {
						count = 99
					}
					for j := 0; j < count; j++ {
						execOrFatal(t, h.db, `INSERT INTO page_board_files(id,board_page_id,sha256,content_type,width,height,byte_size,created_at) SELECT ?,?,sha256,content_type,width,height,byte_size,created_at FROM page_board_files WHERE id=?`, fmt.Sprintf("workspace-file-%d-%d", i, j), board, file)
					}
				}
			}
			artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "over-quota", 1, raw), 409)
			artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "first", 0, raw), 200)
		})
	}
}
func uploadedImageSize(t *testing.T, h *PageHandler, file string) int {
	t.Helper()
	var size int
	if err := h.db.QueryRow(`SELECT byte_size FROM page_board_files WHERE id=?`, file).Scan(&size); err != nil {
		t.Fatal(err)
	}
	return size
}

func TestBoardFilesRestoredBytesAreValidatedOnRead(t *testing.T) {
	h, ws, user, page := boardFileFixture(t)
	huge := append([]byte(nil), boardRaster(t, false)...)
	binary.BigEndian.PutUint32(huge[16:20], 2049)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for i, data := range [][]byte{[]byte("<svg onload='unsafe-restored-file'/>"), huge, boardRaster(t, true), bytes.Repeat([]byte("x"), maxBoardFileOutput+1)} {
		sha, _, err := storeAttachmentBlob(h.artifactStoragePath, ws, data)
		if err != nil {
			t.Fatal(err)
		}
		file := fmt.Sprint("restored-malformed-", i)
		// Model a restored metadata/blob pair that did not pass the upload decoder.
		execOrFatal(t, h.db, `INSERT INTO page_board_files(id,board_page_id,sha256,content_type,width,height,byte_size,created_at) VALUES(?,?,?,'image/png',4,3,1,'2020-01-01T00:00:00.000Z')`, file, page, sha)
		rr := boardFileRead(t, h, ws, user, "report", file)
		artifactExpect(t, rr, 503)
		if rr.Header().Get("Content-Type") == "image/png" || bytes.Contains(rr.Body.Bytes(), []byte("unsafe-restored-file")) {
			t.Fatal("restored unsafe image bytes escaped validation")
		}
	}
	r := pagesGrantRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.Header.Set(actingAgentSlugHeader, "agent")
	r.SetPathValue("slug", "report")
	r.SetPathValue("fileId", "restored-malformed-0")
	rr := httptest.NewRecorder()
	h.GetBoardFile(rr, r)
	artifactExpect(t, rr, 401)
}

package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersonaStorageFailuresAreNotMissingPersonas(t *testing.T) {
	for _, layer := range []PersonaLayer{PersonaAgent, PersonaCrew} {
		t.Run(string(layer), func(t *testing.T) {
			p := PersonaPaths{AgentDir: t.TempDir(), CrewDir: t.TempDir()}
			path, err := resolvePersonaPath(p, layer)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPersona(p); err == nil {
				t.Fatal("directory read treated as empty persona")
			}
			if err := WritePersona(p, layer, "replacement"); err == nil {
				t.Fatal("directory replaced by persona")
			}
			if err := ResetPersona(p, layer); err == nil {
				t.Fatal("nonempty directory silently removed")
			}
			if _, err := os.Stat(filepath.Join(path, "keep")); err != nil {
				t.Fatal("existing data lost", err)
			}
			if layer == PersonaAgent {
				if wrote, err := BackfillFromLegacy(p, "legacy"); err == nil || wrote {
					t.Fatalf("failed backfill reported success: %v %v", wrote, err)
				}
			}
		})
	}
}

func TestPersonaRefusesUnavailableParentsAndLocks(t *testing.T) {
	for _, kind := range []string{"parent is file", "lock is directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			p := PersonaPaths{AgentDir: filepath.Join(dir, "memory")}
			if kind == "parent is file" {
				if err := os.WriteFile(p.AgentDir, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(p.AgentPath()+".lock", 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := WritePersona(p, PersonaAgent, "new"); err == nil {
				t.Fatal("unavailable persona storage accepted")
			}
			if err := ResetPersona(p, PersonaAgent); err == nil {
				t.Fatal("reset silently ignored unavailable lock")
			}
			if wrote, err := BackfillFromLegacy(p, "legacy"); err == nil || wrote {
				t.Fatalf("backfill silently ignored unavailable storage: %v %v", wrote, err)
			}
		})
	}
	for _, layer := range []PersonaLayer{PersonaAgent, PersonaCrew, "invalid"} {
		if err := WritePersona(PersonaPaths{}, layer, "value"); err == nil {
			t.Fatal("missing persona location accepted")
		}
		if err := ResetPersona(PersonaPaths{}, layer); err == nil {
			t.Fatal("reset missing location accepted")
		}
	}
	if wrote, err := BackfillFromLegacy(PersonaPaths{}, "legacy"); err == nil || wrote {
		t.Fatalf("backfilled without location: %v %v", wrote, err)
	}
	if p, err := LoadPersona(PersonaPaths{AgentDir: t.TempDir()}); err != nil || p.Content != "" {
		t.Fatalf("fresh solo persona: %+v %v", p, err)
	}
}

func TestDurableRootWriteFailuresCleanUpTemporaryFiles(t *testing.T) {
	if err := WriteFileDurableRoot(nil, "file", []byte("new"), 0600); err == nil {
		t.Fatal("nil root accepted")
	}
	for _, rooted := range []bool{false, true} {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if rooted {
			err = WriteFileDurableRoot(root, "target", []byte("new"), 0600)
		} else {
			err = WriteFileDurable(target, []byte("new"), 0600)
		}
		if err == nil {
			t.Fatal("rename onto directory accepted")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "target" {
			t.Fatalf("temporary files leaked: %v %v", entries, err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		if err := WriteFileDurableRoot(root, "target", []byte("new"), 0600); err == nil {
			t.Fatal("closed root accepted")
		}
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(outside, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileNoFollow(link, []byte("replace"), 0600); err == nil {
		t.Fatal("symlink accepted")
	}
	body, err := os.ReadFile(outside)
	if err != nil || string(body) != "preserved" {
		t.Fatalf("symlink target changed: %q %v", body, err)
	}
}

func TestPeerCardHelpersPreserveIdentityAndReportUnavailableStorage(t *testing.T) {
	p := PeerPaths{AgentDir: t.TempDir()}
	if cards, err := ListPeerSlugs(p); err != nil || len(cards) != 0 {
		t.Fatalf("fresh card list: %v %v", cards, err)
	}
	if body, err := LoadPeerCard(p, "", "w"); err != nil || body != "" {
		t.Fatalf("missing identity read: %q %v", body, err)
	}
	if body, err := LoadPeerCardBySlug(p, ""); err != nil || body != "" {
		t.Fatalf("missing slug read: %q %v", body, err)
	}
	if err := DeletePeerCard(p, "", "w"); err != nil {
		t.Fatal(err)
	}
	if err := DeletePeerCardBySlug(p, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.PeersDir(), []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListPeerSlugs(p); err == nil {
		t.Fatal("unreadable peer directory reported empty")
	}
	if err := WritePeerCard(p, "u", "w", "fact"); err == nil {
		t.Fatal("invalid peer directory accepted")
	}
	if err := os.Remove(p.PeersDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.CardPath(UserSlug("u", "w"))+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	if err := WritePeerCard(p, "u", "w", "fact"); err == nil {
		t.Fatal("invalid peer lock accepted")
	}
	if err := os.Remove(p.CardPath(UserSlug("u", "w")) + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := WritePeerCard(p, "u", "w", "own fact"); err != nil {
		t.Fatal(err)
	}
	if body, err := LoadPeerCardBySlug(p, UserSlug("u", "w")); err != nil || body != "own fact" {
		t.Fatalf("slug lookup failed: %q %v", body, err)
	}
	if err := os.Remove(p.CardPath(UserSlug("u", "w"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.CardPath(UserSlug("u", "w")), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPeerCardBySlug(p, UserSlug("u", "w")); err == nil {
		t.Fatal("directory card reported empty")
	}
}

func TestUserModelReportsStorageAndPurgeFailures(t *testing.T) {
	p := UserModelPaths{SharedDir: t.TempDir()}
	if err := os.WriteFile(p.UsersDir(), []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteUserModel(p, "u", "w", "fact"); err == nil {
		t.Fatal("unavailable user directory accepted")
	}
	if _, err := ListUserModelSlugs(p); err == nil {
		t.Fatal("unavailable user directory reported empty")
	}
	if n, err := DeleteUserModelEverywhere(t.TempDir(), ""); err != nil || n != 0 {
		t.Fatalf("empty purge was not idle: %d %v", n, err)
	}
}

func TestUserModelPurgeContinuesPastOneUnavailableLocation(t *testing.T) {
	base := t.TempDir()
	slug := UserSlug("u", "w")
	good := UserModelPaths{SharedDir: filepath.Join(base, "workspace", "shared", ".memory")}
	if err := WriteUserModel(good, "u", "w", "erase me"); err != nil {
		t.Fatal(err)
	}
	bad := UserModelPaths{SharedDir: filepath.Join(base, "crews", "crew", "shared", ".memory")}
	if err := os.MkdirAll(bad.ModelPath(slug), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad.ModelPath(slug), "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "crews", "not-a-crew"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := DeleteUserModelEverywhere(base, slug); err == nil || n != 1 {
		t.Fatalf("partial purge lost its error or count: %d %v", n, err)
	}
	if _, err := os.Stat(good.ModelPath(slug)); !os.IsNotExist(err) {
		t.Fatalf("unrelated valid model not erased: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bad.ModelPath(slug), "keep")); err != nil {
		t.Fatal("unrelated directory content removed", err)
	}
	base = t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "crews"), []byte("unavailable directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := DeleteUserModelEverywhere(base, slug); err == nil || n != 0 {
		t.Fatalf("unavailable crew listing ignored: %d %v", n, err)
	}
}

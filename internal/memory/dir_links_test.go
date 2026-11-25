package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// The agent's memory directory lives in a mount the agent can write. A link
// placed there must not make the server write or delete outside it.
func TestQuarantineDoesNotWriteThroughALink(t *testing.T) {
	t.Run("quarantine directory is a link", func(t *testing.T) {
		agentMem, victim := t.TempDir(), t.TempDir()
		if err := os.Symlink(victim, filepath.Join(agentMem, ".quarantine")); err != nil {
			t.Fatal(err)
		}
		_, _, err := Quarantine(agentMem, filepath.Join(agentMem, "x.md"), "agent-controlled body", &ScanHit{})
		if err == nil {
			t.Error("quarantine through a link reported success")
		}
		if entries, _ := os.ReadDir(victim); len(entries) != 0 {
			t.Fatalf("quarantine wrote %d file(s) outside the memory directory", len(entries))
		}
	})
	t.Run("memory directory is a link", func(t *testing.T) {
		parent, victim := t.TempDir(), t.TempDir()
		mem := filepath.Join(parent, ".memory")
		if err := os.Symlink(victim, mem); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Quarantine(mem, filepath.Join(mem, "x.md"), "body", &ScanHit{}); err == nil {
			t.Error("quarantine through a linked memory directory reported success")
		}
		if entries, _ := os.ReadDir(victim); len(entries) != 0 {
			t.Fatalf("quarantine wrote %d file(s) outside", len(entries))
		}
	})
	t.Run("a normal quarantine still works", func(t *testing.T) {
		agentMem := t.TempDir()
		_, sha, err := Quarantine(agentMem, filepath.Join(agentMem, "x.md"), "body", &ScanHit{Category: "c", Pattern: "p"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(agentMem, ".quarantine", sha+".md")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestResetPersonaAndEraseDoNotDeleteThroughALink(t *testing.T) {
	parent := t.TempDir()
	victim := filepath.Join(parent, "victim")
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PERSONA.md", "subject.md"} {
		if err := os.WriteFile(filepath.Join(victim, name), []byte("not the agent's"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mem := filepath.Join(parent, ".memory")
	if err := os.Symlink(victim, mem); err != nil {
		t.Fatal(err)
	}
	if err := ResetPersona(PersonaPaths{AgentDir: mem}, PersonaAgent); err == nil {
		t.Error("persona reset through a link reported success")
	}
	if err := removeUnderLock(filepath.Join(mem, "subject.md"), "users"); err == nil {
		t.Error("erase through a link reported success")
	}
	for _, name := range []string{"PERSONA.md", "subject.md"} {
		if _, err := os.Stat(filepath.Join(victim, name)); err != nil {
			t.Fatalf("%s outside the memory directory was deleted: %v", name, err)
		}
	}
}

func TestResetPersonaAndEraseStillWork(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "PERSONA.md")
	subject := filepath.Join(dir, "subject.md")
	for _, p := range []string{persona, subject} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ResetPersona(PersonaPaths{AgentDir: dir}, PersonaAgent); err != nil {
		t.Fatal(err)
	}
	if err := removeUnderLock(subject, "users"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{persona, subject, subject + lockSuffix} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
	// A layer that was never written is already reset.
	if err := ResetPersona(PersonaPaths{AgentDir: filepath.Join(dir, "absent")}, PersonaAgent); err != nil {
		t.Fatalf("missing directory: %v", err)
	}
}

// The memory directory sits at <base>/crews/<id>/agents/<slug>/.memory. A
// link placed higher up the chain (the agent's own agents/<slug>, or agents)
// is treated exactly like one on .memory itself.
func TestMemoryOpsRefuseLinksInTheParentChain(t *testing.T) {
	for _, linkAt := range []string{"agents/a1", "agents"} {
		t.Run(linkAt, func(t *testing.T) {
			base := t.TempDir()
			crew := filepath.Join(base, "crews", "c1")
			victim := filepath.Join(t.TempDir(), "elsewhere")
			victimMem := filepath.Join(victim, ".memory")
			if linkAt == "agents" {
				victimMem = filepath.Join(victim, "a1", ".memory")
			}
			if err := os.MkdirAll(victimMem, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"PERSONA.md", "subject.md"} {
				if err := os.WriteFile(filepath.Join(victimMem, name), []byte("not the agent's"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(crew, filepath.Dir(linkAt)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, filepath.Join(crew, linkAt)); err != nil {
				t.Fatal(err)
			}
			mem := filepath.Join(crew, "agents", "a1", ".memory")

			if err := ResetPersona(PersonaPaths{AgentDir: mem}, PersonaAgent); err == nil {
				t.Error("persona reset through a linked parent reported success")
			}
			if err := removeUnderLock(filepath.Join(mem, "subject.md"), "users"); err == nil {
				t.Error("erase through a linked parent reported success")
			}
			before, _ := os.ReadDir(victimMem)
			_, _, qerr := Quarantine(mem, filepath.Join(mem, "x.md"), "agent-controlled body", &ScanHit{})
			if qerr == nil {
				t.Error("quarantine through a linked parent reported success")
			}
			after, _ := os.ReadDir(victimMem)
			if len(after) != len(before) {
				t.Errorf("quarantine wrote outside the crew directory: %d -> %d entries", len(before), len(after))
			}
			for _, name := range []string{"PERSONA.md", "subject.md"} {
				if _, err := os.Stat(filepath.Join(victimMem, name)); err != nil {
					t.Errorf("%s outside the crew directory was deleted: %v", name, err)
				}
			}
		})
	}
}

// A link that stays inside the same crew directory is allowed, and an
// installation whose crew directory is itself a link to another disk works.
func TestMemoryOpsAllowLinksInsideTheCrewAndLinkedCrewDirectory(t *testing.T) {
	disk := t.TempDir()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "crews"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(disk, "c1")
	if err := os.MkdirAll(filepath.Join(real, "agents", "a2", ".memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "crews", "c1")); err != nil {
		t.Fatal(err)
	}
	// agents/a1 is an alias of agents/a2 inside the same crew.
	if err := os.Symlink("a2", filepath.Join(real, "agents", "a1")); err != nil {
		t.Fatal(err)
	}
	mem := filepath.Join(base, "crews", "c1", "agents", "a1", ".memory")
	if _, _, err := Quarantine(mem, filepath.Join(mem, "x.md"), "body", &ScanHit{Category: "c", Pattern: "p"}); err != nil {
		t.Fatalf("quarantine in a linked crew directory: %v", err)
	}
	persona := filepath.Join(real, "agents", "a2", ".memory", "PERSONA.md")
	if err := os.WriteFile(persona, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResetPersona(PersonaPaths{AgentDir: mem}, PersonaAgent); err != nil {
		t.Fatalf("persona reset in a linked crew directory: %v", err)
	}
	if _, err := os.Stat(persona); !os.IsNotExist(err) {
		t.Errorf("persona not removed: %v", err)
	}
}

// Peer cards and user models are written under the agent's and the crew's
// memory directories: a link anywhere on that chain, including the peers/
// and users/ directories, must not move the write outside the crew.
func TestPeerAndUserModelWritesRefuseLinks(t *testing.T) {
	base := t.TempDir()
	crew := filepath.Join(base, "crews", "c1")
	outside := t.TempDir()
	for _, d := range []string{filepath.Join(crew, "agents", "a1", ".memory"), filepath.Join(crew, "shared", ".memory")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// peers/ in the agent's memory and users/ in the crew's are links out.
	if err := os.Symlink(outside, filepath.Join(crew, "agents", "a1", ".memory", "peers")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(crew, "shared", ".memory", "users")); err != nil {
		t.Fatal(err)
	}
	peerErr := WritePeerCard(PeerPaths{AgentDir: filepath.Join(crew, "agents", "a1", ".memory")}, "u1", "w1", "a card")
	userErr := WriteUserModel(UserModelPaths{SharedDir: filepath.Join(crew, "shared", ".memory")}, "u1", "w1", "a model")
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("%d file(s) were written outside the crew directory (peer err %v, user err %v)", len(entries), peerErr, userErr)
	}
	if peerErr == nil || userErr == nil {
		t.Errorf("writes through links reported success: %v %v", peerErr, userErr)
	}
}

// The agent can swap a link into the chain between any two steps of a write.
// The write must not depend on a check made earlier: swap the directory for a
// link at the one moment a check-then-write implementation is exposed (the
// temporary file exists, the rename is next) and nothing may land outside.
func TestMemoryWriteSurvivesALinkSwappedInMidWrite(t *testing.T) {
	base := t.TempDir()
	crew := filepath.Join(base, "crews", "c1")
	agentMem := filepath.Join(crew, "agents", "a1", ".memory")
	peers := filepath.Join(agentMem, "peers")
	if err := os.MkdirAll(peers, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	swapped := 0
	afterDurableTemp = func() {
		swapped++
		if err := os.Rename(peers, peers+".real"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(outside, peers); err != nil {
			t.Error(err)
		}
	}
	defer func() { afterDurableTemp = nil }()
	err := WritePeerCard(PeerPaths{AgentDir: agentMem}, "u1", "w1", "a card")
	if swapped != 1 {
		t.Fatalf("hook ran %d times", swapped)
	}
	if err == nil {
		t.Error("a write whose directory was swapped for an outside link reported success")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("%d file(s) landed outside the crew directory", len(entries))
	}
}

// What follows .memory is chosen by the agent. A directory named
// crews/<id>/agents inside it, behind a link, must not move the crew boundary
// into the agent's own tree.
func TestCrewBoundaryIsNotMovedByNamesInsideMemory(t *testing.T) {
	base := t.TempDir()
	crew := filepath.Join(base, "crews", "c1")
	mem := filepath.Join(crew, "agents", "a1", ".memory")
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "x", "agents", "t", ".memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "x", "agents", "t", ".memory", "PERSONA.md"), []byte("not the agent's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mem, "crews")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(mem, "crews", "x", "agents", "t", ".memory")
	before := func() int {
		n := 0
		_ = filepath.WalkDir(outside, func(string, os.DirEntry, error) error { n++; return nil })
		return n
	}
	count := before()

	if err := WriteFileDurable(filepath.Join(nested, "new.md"), []byte("x"), 0o644); err == nil {
		t.Error("write through the nested link succeeded")
	}
	if err := NewFileLock(filepath.Join(nested, "new.md.lock")).Lock(); err == nil {
		t.Error("lock through the nested link succeeded")
	}
	if err := MkdirAllShared(filepath.Join(nested, "topics")); err == nil {
		t.Error("mkdir through the nested link succeeded")
	}
	if _, _, err := Quarantine(nested, filepath.Join(nested, "x.md"), "body", &ScanHit{}); err == nil {
		t.Error("quarantine through the nested link succeeded")
	}
	if err := ResetPersona(PersonaPaths{AgentDir: nested}, PersonaAgent); err == nil {
		t.Error("persona reset through the nested link succeeded")
	}
	if err := removeUnderLock(filepath.Join(nested, "PERSONA.md"), "users"); err == nil {
		t.Error("erase through the nested link succeeded")
	}
	if got := before(); got != count {
		t.Errorf("the tree outside the crew changed: %d -> %d entries", count, got)
	}
	if _, err := os.Stat(filepath.Join(outside, "x", "agents", "t", ".memory", "PERSONA.md")); err != nil {
		t.Errorf("a file outside the crew was deleted: %v", err)
	}
}

// A storage base that itself contains crews/<id>/agents widens the boundary
// to that outer directory, never past it, and never silently into a
// different crew's tree through a link.
func TestCrewBoundaryWithABaseThatLooksLikeACrewDirectory(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "crews", "outer", "shared", "storage")
	mem := filepath.Join(base, "crews", "c1", "agents", "a1", ".memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(mem, "link")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileDurable(filepath.Join(mem, "link", "f.md"), []byte("x"), 0o644); err == nil {
		t.Error("a link out of the outer directory was followed")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("%d file(s) written outside", len(entries))
	}
	if err := WriteFileDurable(filepath.Join(mem, "ok.md"), []byte("x"), 0o644); err != nil {
		t.Errorf("an ordinary write failed: %v", err)
	}
}

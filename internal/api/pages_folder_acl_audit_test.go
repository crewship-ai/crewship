package api

// Regressions from the live audit of dev3 on 2026-09-13 (folder sharing).

import "testing"

// F3: a folder ACL is for people. An agent's viewer carries its crew so that
// canSeePanel can answer for that crew's own panels; a `crew:` entry on a
// folder must not turn that into folder read or write, and neither may a
// `user:` entry that happens to spell the agent's id, nor the workspace row.
func TestFolderACLReach_NeverReachesAnAgent(t *testing.T) {
	entries := []pageFolderACLRecord{
		{SubjectType: pageSubjectCrew, SubjectID: "crew-engine", CanWrite: true},
		{SubjectType: pageSubjectUser, SubjectID: "agent-1", CanWrite: true},
		{SubjectType: pageSubjectWorkspace, SubjectID: "", CanWrite: true},
	}
	agent := &pageViewer{UserID: "", Role: "", Crews: map[string]bool{"crew-engine": true}}
	if v := folderACLReach(entries, agent); v.read || v.write {
		t.Fatalf("an agent reached the folder through its crew: %+v", v)
	}
	// The same entries do reach a human member of that crew, so the guard is
	// about standing, not about the entries.
	human := &pageViewer{UserID: "frank", Role: "MEMBER", Crews: map[string]bool{"crew-engine": true}}
	if v := folderACLReach(entries, human); !v.read || !v.write {
		t.Fatalf("a human crew member did not reach the folder: %+v", v)
	}
	// A human whose standing could not be read (no role) is treated as no
	// standing: the folder arm fails closed like the rest of the file.
	unknown := &pageViewer{UserID: "frank", Role: "", Crews: map[string]bool{"crew-engine": true}}
	if v := folderACLReach(entries, unknown); v.read || v.write {
		t.Fatalf("a viewer without a workspace role reached the folder: %+v", v)
	}
}

// F1: the no-names label tells the kinds of subject apart. A folder shared
// with one person is not "shared with a crew".
func TestFolderSharedLabel_TellsPeopleAndCrewsApart(t *testing.T) {
	user := pageFolderACLRecord{SubjectType: pageSubjectUser, SubjectID: "u1"}
	crew := pageFolderACLRecord{SubjectType: pageSubjectCrew, SubjectID: "c1"}
	everyone := pageFolderACLRecord{SubjectType: pageSubjectWorkspace}
	for _, tc := range []struct {
		name    string
		entries []pageFolderACLRecord
		want    string
	}{
		{"nothing", nil, pageFolderSharedNone},
		{"one person", []pageFolderACLRecord{user}, pageFolderSharedPeople},
		{"one crew", []pageFolderACLRecord{crew}, pageFolderSharedCrews},
		{"person and crew", []pageFolderACLRecord{user, crew}, pageFolderSharedPeopleAndCrews},
		{"everyone wins", []pageFolderACLRecord{user, crew, everyone}, pageFolderSharedWorkspace},
	} {
		if got := folderSharedLabel(tc.entries); got != tc.want {
			t.Errorf("%s: shared = %q, want %q", tc.name, got, tc.want)
		}
	}
}

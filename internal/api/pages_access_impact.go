package api

import (
	"context"
	"strings"
)

// These are independent capabilities. In particular panel production is not a
// position in the human Read < Comment < Edit ladder.
type pageAccessPermissions struct {
	Read    bool `json:"read"`
	Comment bool `json:"comment"`
	Edit    bool `json:"edit"`
	Produce bool `json:"produce"`
}

type pageAccessSource struct {
	Path        string                `json:"path"`
	Inherited   bool                  `json:"inherited"`
	Permissions pageAccessPermissions `json:"permissions"`
}

func (p *pageAccessPermissions) include(other pageAccessPermissions) {
	p.Read = p.Read || other.Read
	p.Comment = p.Comment || other.Comment
	p.Edit = p.Edit || other.Edit
	p.Produce = p.Produce || other.Produce
}

// decoratePageAccess is pure over the same bulk-loaded, already redacted paths
// that power the existing access endpoint. It never adds a hidden subject/path.
func decoratePageAccess(subjects []pageAccessSubjectWire, rec *pageRecord, ws *pageAccessWorkspace, acl map[string][]pageFolderACLRecord) {
	viewers := make(map[string]*pageViewer, len(ws.Members))
	for _, m := range ws.Members {
		v := &pageViewer{UserID: m.ID, Role: m.Role, Crews: map[string]bool{}}
		for _, crew := range ws.CrewsOf[m.ID] {
			v.Crews[crew] = true
		}
		viewers[m.ID] = v
	}
	for i := range subjects {
		s := &subjects[i]
		s.EffectivePermissions = pageAccessPermissions{}
		s.Sources = make([]pageAccessSource, 0, len(s.Paths))
		for _, path := range s.Paths {
			p := pageAccessPermissions{Read: true}
			switch {
			case path == pageReachOwner || path == pageReachRole || strings.HasPrefix(path, pageReachCrew):
				p.Comment = true
				p.Edit = true
			case path == pageReachGrantPage+pageGrantComment:
				p.Comment = true
			case path == pageReachGrantPage+pageGrantWrite:
				p.Comment = s.SubjectType != pageSubjectAgent
				p.Edit = true
			case path == pageReachGrantPage+pageGrantProduce:
				p.Produce = true
			case strings.HasPrefix(path, pageReachFolder):
				if s.SubjectType == pageSubjectUser {
					p.Edit = folderACLReach(acl[rec.FolderID], viewers[s.SubjectID]).write
				} else if s.SubjectType == pageSubjectCrew {
					for _, entry := range acl[rec.FolderID] {
						if entry.SubjectType == pageSubjectCrew && entry.SubjectID == s.SubjectID && entry.CanWrite {
							p.Edit = true
						}
					}
				}
				p.Comment = p.Edit
			}
			s.Sources = append(s.Sources, pageAccessSource{Path: path, Inherited: strings.HasPrefix(path, pageReachFolder), Permissions: p})
			s.EffectivePermissions.include(p)
		}
	}
}

type pageAccessImpactSnapshot struct {
	Subjects []pageAccessSubjectWire `json:"subjects"`
	Withheld bool                    `json:"withheld"`
}

// No per-subject query: use exactly the same live-grant and withheld projection
// as PageAccess. Both snapshots belong to the final fenced writer transaction.
func (h *PageHandler) catalogAccessSnapshot(ctx context.Context, wsID string, rec *pageRecord, viewer *pageViewer) (pageAccessImpactSnapshot, error) {
	var out pageAccessImpactSnapshot
	ws, err := h.loadAccessWorkspace(ctx, wsID)
	if err != nil {
		return out, err
	}
	panels, err := h.loadPanels(ctx, wsID, rec.ID)
	if err != nil {
		return out, err
	}
	grants, err := h.loadPageGrantRecords(ctx, wsID, rec)
	if err != nil {
		return out, err
	}
	folders, err := h.loadFoldersIn(ctx, wsID)
	if err != nil {
		return out, err
	}
	acl, err := h.loadFolderACLIn(ctx, wsID)
	if err != nil {
		return out, err
	}
	out.Subjects = h.pageAccessSubjects(rec, ws, panels, grants, folders, acl, viewer)
	decoratePageAccess(out.Subjects, rec, ws, acl)
	if out.Subjects == nil {
		out.Subjects = []pageAccessSubjectWire{}
	}
	for _, s := range out.Subjects {
		if s.SubjectID == pageAccessWithheld {
			out.Withheld = true
		}
	}
	return out, nil
}

type pageAccessImpactChange struct {
	SubjectType string                `json:"subject_type"`
	SubjectID   string                `json:"subject_id"`
	Label       string                `json:"label,omitempty"`
	Before      pageAccessPermissions `json:"before"`
	After       pageAccessPermissions `json:"after"`
}

func pageAccessChanges(before, after pageAccessImpactSnapshot) []pageAccessImpactChange {
	out := []pageAccessImpactChange{}
	old := map[string]pageAccessSubjectWire{}
	for _, s := range before.Subjects {
		old[s.SubjectType+"/"+s.SubjectID] = s
	}
	for _, s := range after.Subjects {
		key := s.SubjectType + "/" + s.SubjectID
		b := old[key]
		delete(old, key)
		if b.EffectivePermissions != s.EffectivePermissions {
			out = append(out, pageAccessImpactChange{s.SubjectType, s.SubjectID, s.Label, b.EffectivePermissions, s.EffectivePermissions})
		}
	}
	// Walk the original stable order rather than the map, so reviews/digests
	// cannot change because of Go map iteration.
	for _, s := range before.Subjects {
		if _, ok := old[s.SubjectType+"/"+s.SubjectID]; ok {
			out = append(out, pageAccessImpactChange{s.SubjectType, s.SubjectID, s.Label, s.EffectivePermissions, pageAccessPermissions{}})
		}
	}
	return out
}

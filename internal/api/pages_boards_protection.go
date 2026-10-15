package api

import (
	"errors"

	"github.com/crewship-ai/crewship/internal/pages"
)

// protectBoardHumanElements is deliberately conservative. Owning an element
// never authorizes changing a protected element indirectly through a shared
// group, binding, label container or ancestor frame.
func protectBoardHumanElements(current, next pages.BoardScene, batch pages.BoardBatch, authors map[string]boardElementAuthor, agentID string) error {
	if err := pages.ValidateBoardScene(current); err != nil {
		return err
	}
	changed := map[string]bool{}
	for _, upsert := range batch.Upserts {
		element, err := pages.ValidateBoardElement(upsert.Element)
		if err != nil {
			return err
		}
		changed[element.ID] = true
	}
	for _, deleted := range batch.Deletes {
		changed[deleted.ID] = true
	}
	denied := errors.New("agent may change only its own independent board elements")
	for id := range changed {
		if _, present := current.Records[id]; present && authors[id].AgentID != agentID {
			return denied
		}
	}
	parse := func(scene pages.BoardScene) (map[string]pages.BoardElement, error) {
		elements := map[string]pages.BoardElement{}
		for id, record := range scene.Records {
			if record.Deleted {
				continue
			}
			element, err := pages.ValidateBoardElement(record.Element)
			if err != nil {
				return nil, err
			}
			elements[id] = element
		}
		return elements, nil
	}
	before, err := parse(current)
	if err != nil {
		return err
	}
	after, err := parse(next)
	if err != nil {
		return err
	}
	protected := map[string]bool{}
	groups := map[string]bool{}
	for id, element := range before {
		if authors[id].AgentID != agentID {
			protected[id] = true
			for _, group := range element.GroupIDs {
				groups[group] = true
			}
		}
	}
	refs := func(element pages.BoardElement) []string {
		ids := []string{}
		if element.FrameID != nil {
			ids = append(ids, *element.FrameID)
		}
		if element.ContainerID != nil {
			ids = append(ids, *element.ContainerID)
		}
		for _, bound := range element.BoundElements {
			ids = append(ids, bound.ID)
		}
		for _, binding := range []*pages.BoardBinding{element.StartBinding, element.EndBinding} {
			if binding != nil {
				ids = append(ids, binding.ElementID)
			}
		}
		return ids
	}
	for _, elements := range []map[string]pages.BoardElement{before, after} {
		for id, element := range elements {
			if changed[id] {
				for _, group := range element.GroupIDs {
					if groups[group] {
						return denied
					}
				}
				for _, ref := range refs(element) {
					if protected[ref] {
						return denied
					}
				}
			}
			if !protected[id] {
				continue
			}
			for _, ref := range refs(element) {
				if changed[ref] {
					return denied
				}
			}
			parent := element.FrameID
			for parent != nil {
				if changed[*parent] {
					return denied
				}
				ancestor, ok := elements[*parent]
				if !ok {
					break
				}
				parent = ancestor.FrameID
			}
		}
	}
	return nil
}

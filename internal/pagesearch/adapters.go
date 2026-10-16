package pagesearch

import "github.com/crewship-ai/crewship/internal/slides"

// DocumentBlock is accepted only from the existing trusted managed read over
// private IPC, never a public client projection. Its text/hash were computed
// from validated Yjs content by the managed service. Parent aggregate text is
// deliberately ignored; persisted blocks bind every outline entry, not just hits.
type DocumentBlock struct {
	ID     string
	Type   string
	Parent string
	Hash   string
	Text   string
}

func FromDocument(source SourceTag, outline []DocumentBlock, persisted []BlockHash) (Projection, error) {
	projection := Projection{AdapterVersion: AdapterVersion, Source: source, Segments: []Segment{}}
	if err := source.Validate(); err != nil {
		return projection, err
	}
	if source.Type != "document" {
		return projection, ErrInvalid
	}
	hash, err := DocumentHash(persisted)
	if err != nil {
		return projection, err
	}
	if hash != source.Hash || len(outline) != len(persisted) {
		return projection, ErrInvalid
	}
	known := make(map[string]string, len(persisted))
	for _, block := range persisted {
		known[block.ID] = block.Hash
	}
	seen := make(map[string]bool, len(outline))
	for _, block := range outline {
		if seen[block.ID] || known[block.ID] != block.Hash || !identifier.MatchString(block.ID) || block.Parent != "" && (!identifier.MatchString(block.Parent) || known[block.Parent] == "" || !seen[block.Parent]) {
			return projection, ErrInvalid
		}
		seen[block.ID] = true
		switch block.Type {
		case "paragraph", "heading", "codeBlock":
			if block.Type == "codeBlock" && source.Schema != 3 {
				return projection, ErrUnsupported
			}
			projection.Segments = append(projection.Segments, Segment{Kind: block.Type, TargetID: block.ID, ParentID: block.Parent, Text: block.Text})
		case "table", "tableRow", "tableCell", "tableHeader", "bulletList", "orderedList", "listItem":
			// Their computed text repeats descendants. Do not index it.
		default:
			return projection, ErrUnsupported
		}
	}
	return projection, projection.Validate()
}

func FromSlides(source SourceTag, canonical []byte) (Projection, error) {
	projection := Projection{AdapterVersion: AdapterVersion, Source: source, Segments: []Segment{}}
	if err := source.Validate(); err != nil {
		return projection, err
	}
	if source.Type != "slides" {
		return projection, ErrInvalid
	}
	deck, err := slides.Decode(canonical)
	if err != nil {
		return projection, err
	}
	hash, err := slides.DeckHash(deck)
	if err != nil {
		return projection, err
	}
	if deck.DeckID != source.PageID || deck.SchemaVersion != source.Schema || hash != source.Hash {
		return projection, ErrInvalid
	}
	byID := make(map[string]slides.Slide, len(deck.Slides))
	for _, slide := range deck.Slides {
		byID[slide.ID] = slide
	}
	for _, id := range deck.SlideOrder {
		slide := byID[id]
		for _, element := range slide.Elements {
			switch element.Type {
			case "text":
				projection.Segments = append(projection.Segments, Segment{Kind: "text", TargetID: element.ID, ParentID: id, Text: element.Text.Value})
			case "table":
				for _, row := range element.Table.Cells {
					for _, cell := range row {
						projection.Segments = append(projection.Segments, Segment{Kind: "table-cell", TargetID: element.ID, ParentID: id, Text: cell})
					}
				}
			case "shape", "image": // No alt text, filenames, asset metadata, OCR or fetch.
			default:
				return projection, ErrUnsupported
			}
		}
		if slide.Notes != "" {
			projection.Segments = append(projection.Segments, Segment{Kind: "notes", TargetID: id, Text: slide.Notes})
		}
	}
	return projection, projection.Validate()
}

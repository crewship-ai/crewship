package memory

import (
	"strings"
)

const defaultChunkSize = 500

// Chunk represents a section of a markdown file.
type Chunk struct {
	File      string
	LineStart int
	LineEnd   int
	Content   string
}

// ChunkMarkdown splits markdown content into chunks, using ## headings as
// primary boundaries. Falls back to splitting at ~500 chars when sections
// are too large.
func ChunkMarkdown(filename, content string) []Chunk {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	lines := strings.Split(content, "\n")
	var chunks []Chunk
	var currentLines []string
	sectionStart := 1

	for i, line := range lines {
		lineNum := i + 1

		// Split on ## headings (but not the very first line)
		if strings.HasPrefix(line, "## ") && len(currentLines) > 0 {
			text := strings.Join(currentLines, "\n")
			chunks = append(chunks, splitLargeChunk(filename, sectionStart, lineNum-1, text)...)
			currentLines = nil
			sectionStart = lineNum
		}

		currentLines = append(currentLines, line)
	}

	// Flush remaining
	if len(currentLines) > 0 {
		text := strings.Join(currentLines, "\n")
		chunks = append(chunks, splitLargeChunk(filename, sectionStart, len(lines), text)...)
	}

	return chunks
}

// splitLargeChunk breaks a chunk into smaller pieces if it exceeds defaultChunkSize chars.
func splitLargeChunk(filename string, startLine, endLine int, content string) []Chunk {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	if len(content) <= defaultChunkSize {
		return []Chunk{{
			File:      filename,
			LineStart: startLine,
			LineEnd:   endLine,
			Content:   content,
		}}
	}

	// Split at paragraph boundaries (double newlines) for natural breaks
	paragraphs := strings.Split(content, "\n\n")
	var chunks []Chunk
	var buf strings.Builder
	chunkStart := startLine
	currentLine := startLine

	for _, para := range paragraphs {
		paraLines := strings.Count(para, "\n") + 1

		if buf.Len()+len(para) > defaultChunkSize && buf.Len() > 0 {
			chunks = append(chunks, Chunk{
				File:      filename,
				LineStart: chunkStart,
				LineEnd:   currentLine - 1,
				Content:   strings.TrimSpace(buf.String()),
			})
			buf.Reset()
			chunkStart = currentLine
		}

		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(para)
		currentLine += paraLines + 1 // +1 for the blank line between paragraphs
	}

	if buf.Len() > 0 {
		chunks = append(chunks, Chunk{
			File:      filename,
			LineStart: chunkStart,
			LineEnd:   endLine,
			Content:   strings.TrimSpace(buf.String()),
		})
	}

	return chunks
}

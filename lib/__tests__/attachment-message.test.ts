import { describe, it, expect } from "vitest"

import {
  composeMessageWithAttachments,
  sendableAttachments,
  type OutgoingAttachment,
} from "@/lib/attachment-message"

/** Shorthand for a finished upload the composer would hand us. */
const ready = (name: string, path: string): OutgoingAttachment => ({
  name,
  path,
  status: "ready",
})

describe("composeMessageWithAttachments", () => {
  const cases: Array<{
    name: string
    text: string
    attachments: OutgoingAttachment[]
    want: string
  }> = [
    {
      name: "no attachments: the text goes out byte-identical, with no block",
      text: "just a question",
      attachments: [],
      want: "just a question",
    },
    {
      name: "no attachments, multi-line text: untouched",
      text: "line one\n\nline two",
      attachments: [],
      want: "line one\n\nline two",
    },
    {
      name: "one attachment",
      text: "have a look at this",
      attachments: [ready("report.pdf", "attachments/chat-1/report.pdf")],
      want:
        "have a look at this\n\n" +
        "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        "- attachments/chat-1/report.pdf",
    },
    {
      name: "several attachments list one per line, in composer order",
      text: "the three invoices",
      attachments: [
        ready("a.pdf", "attachments/chat-1/a.pdf"),
        ready("b.pdf", "attachments/chat-1/b.pdf"),
        ready("c.pdf", "attachments/chat-1/c.pdf"),
      ],
      want:
        "the three invoices\n\n" +
        "I've attached 3 files to this message. The paths are relative to your working directory:\n\n" +
        "- attachments/chat-1/a.pdf\n" +
        "- attachments/chat-1/b.pdf\n" +
        "- attachments/chat-1/c.pdf",
    },
    {
      name: "a filename with spaces is carried verbatim — one path per line is the delimiter",
      text: "here",
      attachments: [ready("Q3 invoice final.pdf", "attachments/chat-1/Q3 invoice final.pdf")],
      want:
        "here\n\n" +
        "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        "- attachments/chat-1/Q3 invoice final.pdf",
    },
    {
      name: "quotes and brackets survive unescaped",
      text: "here",
      attachments: [
        ready(`screen "shot" [1].png`, `attachments/chat-1/screen "shot" [1].png`),
      ],
      want:
        "here\n\n" +
        "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        `- attachments/chat-1/screen "shot" [1].png`,
    },
    {
      name: "empty text with one attachment: the block stands alone, no leading blank lines",
      text: "",
      attachments: [ready("photo.jpg", "attachments/chat-1/photo.jpg")],
      want:
        "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        "- attachments/chat-1/photo.jpg",
    },
    {
      name: "whitespace-only text with attachments behaves like empty text",
      text: "   \n ",
      attachments: [ready("photo.jpg", "attachments/chat-1/photo.jpg")],
      want:
        "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        "- attachments/chat-1/photo.jpg",
    },
    {
      name: "empty text and no attachments is empty",
      text: "",
      attachments: [],
      want: "",
    },
    {
      name: "an attachment with no path yet is not named",
      text: "here",
      attachments: [{ name: "big.zip", status: "uploading" }],
      want: "here",
    },
    {
      name: "a failed upload is not named",
      text: "here",
      attachments: [{ name: "big.zip", status: "error", path: "attachments/chat-1/big.zip" }],
      want: "here",
    },
    {
      name: "control characters in a path are stripped, not passed through",
      text: "here",
      attachments: [ready("odd.txt", "attachments/chat-1/odd\nnext.txt")],
      want:
        "here\n\n" +
        "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        "- attachments/chat-1/oddnext.txt",
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      expect(composeMessageWithAttachments(c.text, c.attachments)).toBe(c.want)
    })
  }

  it("never reads as a system directive addressed at the model", () => {
    const out = composeMessageWithAttachments("hi", [ready("a.pdf", "attachments/c/a.pdf")])
    // No pseudo-XML/system framing, no imperative instruction voice.
    expect(out).not.toMatch(/<\/?[a-z_]+>/i)
    expect(out).not.toMatch(/\byou (must|should|are required)\b/i)
    expect(out).not.toMatch(/\bsystem\b/i)
    // It does state the path, which is the entire point.
    expect(out).toContain("attachments/c/a.pdf")
  })

  it("is stable: composing twice gives the same string", () => {
    const atts = [ready("a.pdf", "attachments/c/a.pdf")]
    expect(composeMessageWithAttachments("hi", atts)).toBe(
      composeMessageWithAttachments("hi", atts),
    )
  })
})

describe("sendableAttachments", () => {
  it("keeps only finished uploads that have a path", () => {
    const out = sendableAttachments([
      ready("a.pdf", "attachments/c/a.pdf"),
      { name: "b.zip", status: "uploading" },
      { name: "c.zip", status: "error", path: "attachments/c/c.zip" },
      { name: "d.txt", path: "" },
    ])
    expect(out.map((a) => a.name)).toEqual(["a.pdf"])
  })
})

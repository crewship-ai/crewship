declare module "saxen" {
  export class Parser {
    on(event: "openTag", handler: (name: string, attributes: () => Record<string, string>, decode: (text: string) => string, selfClosing: boolean) => void): this
    on(event: "closeTag", handler: (name: string) => void): this
    on(event: "text", handler: (text: string, decode: (text: string) => string) => void): this
    on(event: "cdata", handler: (text: string) => void): this
    on(event: "error" | "warn", handler: (error: Error) => void): this
    on(event: "attention", handler: () => void): this
    parse(xml: string): void
  }
}

export const ENGINE_SHA: string
export interface RegistryCommand { id: string; label: string; location: string; params: string; shortcut: string; mutates: boolean }
export interface ParameterSchema { status: 'exact' | 'unresolved'; schema: false | { type: string; properties: Record<string, { type: string }>; required?: string[]; additionalProperties: false } }
export function parameterSchema(descriptor: string): ParameterSchema
export function classify(command: Pick<RegistryCommand, 'id' | 'location' | 'mutates'>): { group: string; effect: string; permanentlyForbidden: boolean }
export function generate(registry: RegistryCommand[], identity: { path: string; sha256: string }[]): object
export function sourceIdentity(source: string): { path: string; sha256: string }[]
export function main(args: string[]): void

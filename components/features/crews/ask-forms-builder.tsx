"use client"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import { MAX_FIELDS_PER_FORM, MAX_FORMS, type AskForm, type AskFormField } from "@/lib/ask-template"

const control = "w-full rounded-lg border border-border bg-background px-3 py-2 text-sm"
const types = ["text", "textarea", "number", "money", "date", "month", "select", "multiselect", "checkbox", "file", "photo"]
export function AskFormsBuilder({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const [raw, setRaw] = useState(false)
  let forms: AskForm[] = []
  let valid = true
  try { const parsed: unknown = JSON.parse(value || "[]"); if (!Array.isArray(parsed) || parsed.some((f) => !f || typeof f !== 'object' || !Array.isArray(f.fields) || f.fields.some((field: unknown) => !field || typeof field !== 'object' || !('name' in field) || !('type' in field)))) valid = false; else forms = parsed } catch { valid = false }
  const change = (next: AskForm[]) => onChange(JSON.stringify(next, null, 2))
  const update = (index: number, patch: Partial<AskForm>) => change(forms.map((form, n) => n === index ? { ...form, ...patch } : form))
  const field = (index: number, n: number, patch: Partial<AskFormField>) => update(index, { fields: forms[index].fields.map((f, i) => i === n ? { ...f, ...patch } : f) })
  return <section className="space-y-4"><div className="flex items-center justify-between gap-3"><div><h3 className="text-sm font-medium">Chat forms</h3><p className="text-xs text-muted-foreground mt-1">Collect answers before sending a message to this agent.</p></div><Button variant="ghost" size="sm" onClick={() => setRaw((v) => !v)}>{raw ? "Form builder" : "Advanced JSON"}</Button></div>
    {raw || !valid ? <><textarea aria-label="Forms JSON" value={value} onChange={(event) => onChange(event.target.value)} rows={10} className={`${control} font-mono`} />{!valid && <p role="alert" className="text-xs text-destructive">Fix the JSON structure to use the form builder. Your draft is retained.</p>}</> : <>
      {forms.map((form, index) => <section key={index} className="rounded-xl border border-border p-4 space-y-3"><div className="grid sm:grid-cols-2 gap-3"><label className="text-xs">Button label<input className={control} value={form.label} onChange={(event) => update(index, { label: event.target.value })} /></label><label className="text-xs">Form ID<input className={control} value={form.id} onChange={(event) => update(index, { id: event.target.value })} /></label></div>
        <label className="block text-xs">Attachment<select className={control} value={form.attachment || "none"} onChange={(event) => update(index, { attachment: event.target.value })}>{['none', 'optional', 'required'].map((option) => <option key={option}>{option}</option>)}</select></label>
        {form.fields.map((input, n) => <div key={n} className="rounded-lg bg-muted/30 p-3 space-y-2"><div className="grid sm:grid-cols-3 gap-2"><label className="text-xs">Field label<input className={control} value={input.label} onChange={(event) => field(index, n, { label: event.target.value })} /></label><label className="text-xs">Variable<input className={control} value={input.name} onChange={(event) => field(index, n, { name: event.target.value })} /></label><label className="text-xs">Type<select className={control} value={input.type} onChange={(event) => field(index, n, { type: event.target.value })}>{!types.includes(input.type) && <option>{input.type}</option>}{types.map((type) => <option key={type}>{type}</option>)}</select></label></div>
          {(input.type === 'select' || input.type === 'multiselect' || input.type === 'money') && <label className="block text-xs">{input.type === 'money' ? 'Currencies' : 'Options'} (comma separated)<input className={control} value={(input.type === 'money' ? input.currency : input.options)?.join(', ') || ''} onChange={(event) => field(index, n, { [input.type === 'money' ? 'currency' : 'options']: event.target.value.split(',').map((v) => v.trim()) })} /></label>}
          <div className="flex justify-between items-center gap-2"><label className="text-xs flex items-center gap-2"><input type="checkbox" checked={!!input.required} onChange={(event) => field(index, n, { required: event.target.checked })} />Required</label><Button variant="ghost" size="sm" onClick={() => update(index, { fields: form.fields.filter((_, i) => i !== n) })}>Remove field</Button></div>
        </div>)}
        <Button variant="outline" size="sm" disabled={form.fields.length >= MAX_FIELDS_PER_FORM} onClick={() => { let n = form.fields.length + 1; while (form.fields.some((f) => f.name === `field${n}`)) n++; update(index, { fields: [...form.fields, { name: `field${n}`, label: `Field ${n}`, type: 'text' }] }) }}>Add field</Button>
        <label className="block text-xs">Message template<textarea className={control} rows={4} value={form.template} onChange={(event) => update(index, { template: event.target.value })} /></label><p className="text-xs text-muted-foreground">Insert variables such as {'{{field1}}'}. An unanswered optional variable removes its line. Validation runs when you save.</p><Button variant="ghost" size="sm" onClick={() => change(forms.filter((_, i) => i !== index))}>Remove form</Button>
      </section>)}
      <Button variant="outline" size="sm" disabled={forms.length >= MAX_FORMS} onClick={() => { let n = forms.length + 1; while (forms.some((f) => f.id === `form${n}`)) n++; change([...forms, { id: `form${n}`, label: 'New request', attachment: 'none', template: 'Please help with {{field1}}', fields: [{ name: 'field1', label: 'Request', type: 'text', required: true }] }]) }}>Add form</Button>
    </>}
  </section>
}

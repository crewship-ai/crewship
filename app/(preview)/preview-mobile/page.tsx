"use client"

import {
  VariantA, VariantB, VariantC, VariantD, VariantE,
} from "./variants"

export default function PreviewMobilePage() {
  return (
    <div className="p-6 space-y-20 max-w-5xl mx-auto">
      <div className="mb-8">
        <h1 className="text-lg font-bold mb-1">Mobile Navigation Preview</h1>
        <p className="text-sm text-muted-foreground">Klikej na hamburger ikony a prepinac Chat/Sessions/Files pro interakci. Vsechny varianty ukazuji realisticky sessions chat obsah.</p>
      </div>

      {/* Varianta E first -- recommended */}
      <div className="mb-12">
        <div className="inline-block px-2 py-0.5 bg-primary text-primary-foreground text-[10px] font-bold rounded mb-2">DOPORUCENA</div>
        <h2 className="text-sm font-semibold mb-1">Varianta E -- Levy hamburger (agent menu) + pravy hamburger (hlavni nav ze spoda) + Chat/Sessions/Files prepinac</h2>
        <p className="text-xs text-muted-foreground mb-6">Levy hamburger otevre agent podstranky zleva (Overview, Sessions, Files, Runs, Logs, Skills, Credentials, Settings, Debug, History). Pravy hamburger otevre hlavni navigaci ze spoda (Supabase bottom sheet). Breadcrumb: Crews &gt; Pepicek.</p>
        <VariantE />
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-16">
        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta A -- Hamburger vpravo, slide-over zprava</h2>
          <p className="text-xs text-muted-foreground mb-6">Breadcrumb vlevo, hamburger vpravo. Menu vyjede zprava pres obsah.</p>
          <VariantA />
        </div>

        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta B -- Bottom tab bar + sheet zdola</h2>
          <p className="text-xs text-muted-foreground mb-6">iOS styl: hlavni navigace dole (Home, Crews, Agents, Runs, More). More otevre sheet zdola.</p>
          <VariantB />
        </div>

        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta C -- Hamburger vlevo, slide-over zleva</h2>
          <p className="text-xs text-muted-foreground mb-6">Supabase hybrid: hamburger vlevo otevre plne menu zleva se sekcemi. Breadcrumb uprostred.</p>
          <VariantC />
        </div>

        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta D -- Hamburger vpravo, menu ZDOLA (Supabase)</h2>
          <p className="text-xs text-muted-foreground mb-6">Jako Supabase: hamburger vpravo nahore, menu vyjede zespoda nahoru jako bottom sheet. Se sekcemi a profilem.</p>
          <VariantD />
        </div>
      </div>
    </div>
  )
}

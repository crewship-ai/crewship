# Čitelnost automatizace — rešerše trhu, 9. 9. 2026

Evidence k [work orderu](../prd/WORK-ORDER-2026-09-09-ROUTINES-FIX-AND-UX.md) §9.
Podklad výhradně z veřejné oficiální dokumentace, changelogů a firemních blogů.
**Do žádného produktu jsem se nepřihlašoval ani ho netestoval.** Řádky označené
*(inference)* jsou závěr autora, ne doložený fakt.

## 1. Shrnutí „co to dělá" jako datové pole

| Produkt | Doložené chování | Zdroj |
|---|---|---|
| **Temporal** | **Static Summary** — „single-line workflow description", limit **200 bajtů**, markdown; vykresluje se **ve výpisu i v hlavičce detailu**. **Static Details** (20 KB) v detailu, **Current Details** měnitelné za běhu. Popisky nad 120 znaků se ořezávají. | [enriching-ui](https://docs.temporal.io/develop/go/platform/enriching-ui) |
| **Windmill** | **Summary** = „a short, human-readable summary… displayed as a title across Windmill. **If omitted, the UI will use the path by default.**" **Description** = instrukce v auto-generovaném UI. | [flows_quickstart](https://www.windmill.dev/docs/getting_started/flows_quickstart), [script_editor/settings](https://www.windmill.dev/docs/script_editor/settings) |
| **Zapier** | Popisové pole **zrušeno**, nahrazeno poznámkami (Zap note 5 000 znaků, step note 500) s tlačítkem **„Generate with AI"**. Poznámka žije v draftu, dokud se nepublikuje. | [18091355055757](https://help.zapier.com/hc/en-us/articles/18091355055757-Easily-document-your-complete-workflow-with-Zap-notes) |
| **Zapier — autopojmenování** | **Není doložené.** Default je „Untitled Zap" + ruční Rename; autopojmenování se týká jen **kroků**. Vzorec `<Trigger> to <Action>` **necitovat jako fakt**. | [blog/updates/835](https://zapier.com/blog/updates/835/editor-upgrades-add-notes-zaps-and-rename-any-step) |
| **Retool** | **README** na workflow, GFM, viditelné v detailu, v JSON exportu i v source control. | [create-workflows-readme](https://docs.retool.com/workflows/guides/create-workflows-readme) |
| **Power Automate** | Detail ukazuje „name of the flow, its description, the owner, when it was created, the type of flow, the connections used". Na kartách akcí **„Add a note"**. | [overview-manage-cloud-flows](https://learn.microsoft.com/en-us/power-automate/overview-manage-cloud-flows) |
| **UiPath** | Karta procesu: Process name, Description, Recipient folder, Latest publishing date, Published package version, tlačítko Run. | [exploring-processes](https://docs.uipath.com/action-center/automation-cloud/latest/user-guide/exploring-processes) |
| **n8n** | **Popisové pole neexistuje.** Jediná anotace jsou sticky notes na plátně. | [configure-workflow-settings](https://docs.n8n.io/build/manage-workflows/configure-workflow-settings/) |
| **Make** | Poznámky jen na modulech a routách. Popis scénáře doložený není. | [scenario-notes](https://help.make.com/scenario-notes) |
| **LangSmith** | Varovný příklad: „run names **default to the class name of the traced object (e.g., 'ChatOpenAI')**". Přesně náš případ 0/81. | [trace-with-langchain](https://docs.langchain.com/langsmith/trace-with-langchain) |
| **Langfuse** | Konvence: pojmenovat **slovesem napřed** (`classify-intent`, `retrieve-context`), nízká kardinalita (`process-order`, ne `process-order-8945`). | [best-practices](https://langfuse.com/docs/observability/best-practices) |

## 2. První obrazovka pro neautora

| Produkt | Co vede | Zdroj |
|---|---|---|
| **Power Automate** | **Detail flow, ne editor.** Popis, vlastník, typ, konexe, 28denní historie, Analytics. Do designeru se kliká zvlášť. | [overview-manage-cloud-flows](https://learn.microsoft.com/en-us/power-automate/overview-manage-cloud-flows) |
| **Windmill** | Plátno se operátorovi **skryje**: „The recommended way to share scripts and flows with operators is through **auto-generated apps**." Operátor navíc nesmí spouštět previews. | [roles_and_permissions](https://www.windmill.dev/docs/core_concepts/roles_and_permissions) |
| **UiPath** | Orchestrator (provoz) vs **Action Center** (byznysová karta + Run). | viz §1 |
| **Make, n8n, Zapier, Retool, Dify** | Vede **plátno/diagram**. | [16722578092429](https://help.zapier.com/hc/en-us/articles/16722578092429-Use-the-editor-to-build-and-view-your-Zap-workflows) |

**Prózou nevede nikdo.** Nejblíž Power Automate a Windmill.
*(inference: pro nás příležitost, ne riziko — nekopírujeme mezeru, vyplňujeme ji.)*

## 3. Detail běhu

| Produkt | Doložený obsah | Zdroj |
|---|---|---|
| **Temporal** | Hlavička: Status, Start/Close, Duration, Run Id, Workflow Type, Task Queue, Parent, SDK, State Transitions; pod tím Summary & Details; sekce Input and Results, Pending Activities; historie ve čtyřech pohledech; akce včetně **„Start Workflow Like This One"**. Záměr: „understand what's happening, right now… **without needing to interact with the full Event History**". | [web-ui](https://docs.temporal.io/web-ui) |
| **Zapier** | „Find the errored step, then click the **Troubleshoot** tab" (AI vysvětlení) + **Logs**. V editoru **„Go to step"**, které „**auto-select the errored step**". Detail nese **verzi Zapu použitou pro daný běh**. 11 stavů běhu včetně **Needs review**. | [8496037690637](https://help.zapier.com/hc/en-us/articles/8496037690637-How-to-troubleshoot-errors-in-Zap-workflows), [20505304170637](https://help.zapier.com/hc/en-us/articles/20505304170637-Review-run-statuses-in-Zap-workflows) |
| **Power Automate** | „**at least one step shows a red exclamation icon**… **On the right pane, you can see the details of the error and how to fix it**" — sekce **How to fix**. **Resubmit** i hromadně (20 najednou). | [fix-flow-failures](https://learn.microsoft.com/en-us/power-automate/fix-flow-failures) |
| **Dify** | Detail běhu má **přesně tři taby**: `Result` / `Detail` / `Tracing`. Vzhled selhaného uzlu **nedoložený — netvrdit**. | [history-and-logs](https://docs.dify.ai/en/use-dify/debug/history-and-logs) |
| **Windmill** | Stav, **Trigger** (jak byl běh spuštěn), Inputs, důvod selhání, logy, u flow živě aktualizovaný graf. Akce: cancel, re-run, **resume suspended steps**. | [monitor_past_and_future_runs](https://www.windmill.dev/docs/core_concepts/monitor_past_and_future_runs) |
| **n8n** | Stavy Failed / Running / Success / Waiting; u selhání **„Retry with currently saved workflow" / „Retry with original workflow"**; **„Debug in editor"** připne data běhu do prvního uzlu. | [debug-executions](https://docs.n8n.io/build/understand-workflows/understand-executions/debug-executions.md) |
| **Make** | Řádek historie: datum, název, typ triggeru, status, doba, operace, objem dat; **Replay scenario run**; detail otevírá builder **u chybujícího modulu**. | [scenario-history](https://help.make.com/scenario-history) |
| **Retool** | Seznam běhů jen datum/čas/stav. Poctivá věta výrobce: „**A workflow cannot determine if its actions produced the results you expected.**" | [logs](https://docs.retool.com/workflows/concepts/logs) |
| **LangSmith** | Taby Threads / Traces / Runs → boční panel; pohledy Messages (`M`) / Turns (`T`, **deprecated k 31. 10. 2026**) / Details (`D`, default); Waterfall uvnitř trace. Hlavička je **akční, ne metriková** — status, tagy ani metadata v ní doložené nejsou. **Engine** je jediný doložený „skok na chybu": „When Engine identifies the child run that caused the issue, **it opens that exact run**", + 16 kategorií selhání. | [view-traces](https://docs.langchain.com/langsmith/view-traces), [engine](https://docs.langchain.com/langsmith/engine) |
| **Langfuse** | Přepínač Tree / Timeline, Graph view, show/hide scores a metrik. `level`/`statusMessage` **neověřeno — neuvádět**. | [changelog 2025-03-19](https://langfuse.com/changelog/2025-03-19-new-trace-view) |

## 4. Člověk ve smyčce

| Produkt | Kde čeká rozhodnutí | Zdroj |
|---|---|---|
| **Power Automate** | **Tři kanály zároveň**: Outlook, Teams adaptive card, **Action center** (taby Received / Sent / History). Kontext = jediné autorem psané pole `Details`. | [get-started-approvals](https://learn.microsoft.com/en-us/power-automate/get-started-approvals) |
| **UiPath** | **Action Center → Inbox**, taby Pending / Unassigned / Completed, panel Action summary, Comments; „If a different user opens it, it is **read-only**." | [exploring-actions](https://docs.uipath.com/action-center/automation-cloud/latest/user-guide/exploring-actions) |
| **Zapier** | Stav **`Needs review`**; akce Collect Data / Request Approval, konfigurovatelné popisky tlačítek, *Action if reviewer declines*, **due date s vypršením**. Žádná zvláštní schránka. | [38733184458765](https://help.zapier.com/hc/en-us/articles/38733184458765-Use-Human-in-the-Loop-to-pause-Zaps-pending-human-review) |
| **Windmill** | Krok Suspend/Approval/Prompt; approval page s resume/cancel URL, volitelným formulářem a **Description s markdownem**. | [flow_approval](https://www.windmill.dev/docs/flows/flow_approval) |
| **n8n** | **„Send and Wait for Response"** (Slack, Gmail, Teams) s Response Type Approval / Free Text / Custom Form. | [slack/approvals](https://docs.n8n.io/integrations/builtin/app-nodes/n8n-nodes-base.slack/approvals/) |
| **Dify** | Human Input node, kanály Web App a **Email**; reviewer vidí původní dotaz + výstup AI, editovatelná pole a pojmenovaná tlačítka; timeout branch. **Dify nemá schránku** — e-mailový odkaz vyřídí kdokoli, kdo ho drží, **bez účtu**. | [dify.ai/blog/the-human-input-node…](https://dify.ai/blog/the-human-input-node-bringing-human-judgment-into-automated-workflows) |
| **Make** | *Human in the loop* je Enterprise a **v uzavřené betě**. Reálný mechanismus jsou **incomplete executions** (auto-retry **ve výchozím stavu vypnuté**). | [manage-incomplete-executions](https://help.make.com/manage-incomplete-executions) |

## 5. Zdraví a prázdné stavy

| Fakt | Zdroj |
|---|---|
| **n8n Insights** per workflow: total production executions, failed, failure rate, time saved, run time average; default **klouzavé 7denní okno** se srovnáním; **manuální testovací běhy se nezapočítávají**. | [track-usage-with-insights](https://docs.n8n.io/administer/observe-and-log/track-usage-with-insights) |
| **Power Automate**: 28denní historie na detailu. | [monitoring-and-alerting](https://learn.microsoft.com/en-us/power-automate/guidance/coding-guidelines/monitoring-and-alerting) |
| **Zapier**: automatické vypnutí, když „**95 % of its runs result in errors in the last 7 days**", s e-mailem 24–72 h předem. | [8496037690637](https://help.zapier.com/hc/en-us/articles/8496037690637-How-to-troubleshoot-errors-in-Zap-workflows) |
| **Prázdné stavy nedokumentuje nikdo.** Airtable dokumentuje past: „you will not be able to see any tests that were performed as part of the setup process" — nová automatizace vypadá jako nikdy nespuštěná i po testování. | [3199652922](https://support.airtable.com/articles/3199652922-managing-airtable-automations) |

## 6. Kolik ploch má editor

„Plocha" = místo, kam se **naviguje** (tab, stránka, krok průvodce). Kontextové
panely vybraného objektu se nepočítají.

| Produkt | Ploch | Co to je |
|---|---|---|
| **Windmill** | **1** | jedno plátno; Settings je zásuvka, panel kroku má 3 taby |
| **Dify** | **2** | plátno + Run History |
| **Zapier** | **2** | plátno + Zap history; panel kroku 3 taby (Setup / Configure / Test) |
| **n8n** | **3** | Editor / Executions / Evaluations |
| **Make** | **3** | Diagram / History / Incomplete executions |
| **Crewship dnes** | **29** | 5 sekcí průvodce + Code toggle + vnořený 3tabový testovací povrch + … |

**Vícekrokový průvodce pro autoring nepoužívá ani jeden z devíti zkoumaných
produktů.** Trh se pohybuje mezi 1 a 3 plochami.

## 7. Slovesa

| Slovo | Co v odvětví doloženě znamená |
|---|---|
| **Test** | Standardní verb pro kontrolu — **a spouští reálný kód**. Zapier `Test trigger` / `Test step` / `Skip test` s doloženým varováním „**Testing is live and may result in changes made in your app.**" ([18811411817741](https://help.zapier.com/hc/en-us/articles/18811411817741-Test-Zap-steps)). Dále Windmill `Test flow` / `Test this step` / `Test up to step`, Power Automate `Test`, Dify `Test Run` → `Start Run`, Airtable, Retool. |
| **Run** | Ostré spuštění. Make `Run once`, Retool `Run`, n8n `Execute workflow` / `Execute step`. |
| **Preview** | Jen dvakrát a v jiném významu: Windmill „preview job" = kód, který **ještě není nasazený**; Dify `Preview` = průchod koncovým UI. **Neznamená „bez následků".** |
| **Validate** | **Nepoužívá jako plochu ani jeden z devíti produktů.** |
| **Dry run** | **Nepoužívá nikdo.** |

**Režim bez vedlejších účinků v tomto odvětví neexistuje.** Jediné doložené
mocky jsou Power Automate `Enable Static Result` (per akce) a n8n pinned data.

## 8. Sedm vzorců čitelnosti, podle přínosu pro nás

1. **Jednořádkové shrnutí uložené jako pole, viditelné v seznamu i v hlavičce.**
   Temporal `Static Summary` (200 B), Windmill `Summary`. *Pro nás nejlevnější
   vítězství v celé rešerši — pole už máme u 25/27 rutin, jen je schované.*
2. **Lidský název na každém kroku s bezpečným fallbackem.** Windmill: „If
   omitted, the UI will use the path by default." LangSmith ukazuje odvrácenou
   stranu (`ChatOpenAI`). *Bez toho zůstane každý wireframe seznamem ID.*
3. **Prozaická dokumentace přilepená k automatizaci, generovatelná AI.**
   Zapier poznámky s „Generate with AI", Retool README.
4. **Detail není editor.** Power Automate, Windmill.
5. **Selhání se aranžuje, ne jen zobrazuje.** Červený vykřičník → auto-výběr
   chybného kroku → panel „jak to opravit" → akce.
6. **„Přehraj tenhle běh" jako sloveso první třídy.** n8n má nejlepší sémantiku:
   *Retry with currently saved* vs *original workflow* — verzní volba explicitně.
7. **Publikace oddělená od editace, s verzemi.** Retool: „**Only the published
   version is used by Retool**". Detail běhu musí nést verzi, kterou běžel.

## 9. NEKOPÍROVAT

1. **Vícekrokový průvodce pro autoring** — nemá ho nikdo.
2. **Osm jmen pro jednu věc.**
3. **Plátno jako první obrazovka pro neautora** (n8n, Make, Zapier, Retool, Dify).
4. **Make `Run once` bez varování a bez úniku.** Zapierovo varování + `Skip test`
   je správná verze.
5. **Retoolův seznam běhů (datum/čas/stav)** bez byznysového identifikátoru.
6. **Airtable: testy se neobjeví v historii.** Testovací běhy ukazovat odlišené
   a do metrik zdraví je nepočítat.
7. **Technický default místo jména** — `ChatOpenAI`, „Untitled Zap", cesta místo
   Summary. Nikdy prázdné; vždy vygenerovat návrh.
8. **Zapier: poznámky žijí jen v draftu.** U nás musí poznámka patřit rutině,
   ne verzi.
9. **Dify: schvalování přes e-mailový odkaz bez účtu.** Naše rozhodnutí musí
   vést do **autentizované schránky**.
10. **Temporal: 200 znaků na Summary** — shrnutí je věta, ne odstavec.

## 10. Kde nemáme oporu v trhu

Tři věci v návrhu jsou **naše vlastní sázka**, ne převzatý standard.

**10.1 Jednořádkové shrnutí *běhu*** („Nedokončeno — Fakturoid odmítl přihlášení
ve 2. kroku"). **Nedělá to nikdo.** LangSmith používá pro náhled sloupců
**heuristiku, ne AI**; jeho Insights dělají AI shrnutí až **na agregátu** a
u konkrétního běhu se nikdy nezobrazí. *Doporučení: sázku nezahazovat, ale
postavit ji **deterministicky** — věta ze stavu + `name` selhaného kroku +
první řádky chybové hlášky. Model až jako druhá iterace. Deterministická verze
nemůže lhát a nemá latenci.*

**10.2 Prozaická sekce „Co tato rutina dělá" jako primární obsah detailu.**
AI shrnutí na úrovni automatizace precedens má (Zapier). Vedení detailu prózou
místo grafu ne.

**10.3 Auto-rozbalení chybného kroku v běžném detailu běhu.** Dva precedenty
(Zapier „Go to step", LangSmith Engine), ale Engine je samostatný diagnostický
produkt. **Standard je filtruj-a-scanuj.** Držet, ale vědět, že je to menšina.

**Plnou oporu v trhu naopak mají**: pole `summary`, `name` na kroku s fallbackem,
poznámky s AI generováním, oddělení detailu od editoru, publikace s verzemi,
replay s verzní volbou, okno zdraví 7–30 dní a slovesa `Test` / `Run` / `Publish`.

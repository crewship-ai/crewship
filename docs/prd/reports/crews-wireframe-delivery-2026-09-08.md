# Crews & Agents — wireframe a návrh dodělání

2026-09-08 · Dev2 · návrh, nikoliv nasazená aplikace.

[Otevřít klikací wireframe](../wireframes/crews-workspace.html)

Ukázka běží samostatně, bez serveru, s modelovými daty. Zápisy existují
pouze v paměti otevřené stránky a reload je zahodí. Nenahrazuje produkční
Chat, Files, úkoly ani jejich editory; jejich vstupy ukazují cílovou
návaznost v krátkém náhledu.

## Co vyzkoušet

1. **Kodi → Overview:** aktuální práce, tři omezené metriky, výsledky,
   poslední komunikace a krátký souhrn přístupů.
2. **Copy site → Overview / Team:** stejná struktura, navíc složení týmu.
3. **Memory:** znalosti agenta, sdílené znalosti a O mně; detail dokumentu
   odděluje aktuální obsah od historie. Stav historie lze změnit v ukázce.
4. **Edit agent / + Agent:** stejná pole a sekce, zachování draftu při
   přepínání, explicitní uložení, ochrana před zahozením změn.
5. **+ Crew:** název a účel → složení → souhrn. Podrobnosti prostředí
   lze otevřít v souhrnu. Edit crew přímo zpřístupňuje odpovídající pole.
6. **Přepínač stavu nahoře:** aktivní tým, skutečné rozhodnutí, nový
   agent/tým, selhání načítání. Schválení v ukázce odstraní čekající návrh.
7. **Všechny týmy:** souhrn crew, účel a jejich agenti. Sidebar hledá
   mezi ukázkovými týmy a agenty a na telefonu se sklápí.

Náhledy: [agent](../wireframes/crews-workspace-agent.png),
[crew](../wireframes/crews-workspace-crew.png),
[Memory](../wireframes/crews-workspace-memory.png),
[editor](../wireframes/crews-workspace-editor.png),
[mobil](../wireframes/crews-workspace-mobile.png).

## Základní návrhová rozhodnutí

| Místo | Jakou otázku zodpovídá | Co se tam běžně neukazuje |
|---|---|---|
| Všechny týmy | Kde najdu lidi/agenty pro svou práci? | Podrobné logy a katalog každé integrace. |
| Overview | Co dělá, co přinesl, co potřebuje ode mě? | Úplná historie a devět prázdných vazebních karet. |
| Team | Kdo je v crew a jakou má roli? | Osobní profily skutečných uživatelů. |
| Work | Jaké úkoly, výsledky a automatizace patří této entitě? | Další kopie editorů Issues/Routines. |
| Memory | Co je uchováno pro další práci a pro koho to platí? | Konfigurační proměnné serveru jako hlavní obsah. |
| Edit | Co má agent/crew dělat a jaké má mít nastavení? | Provozní timeline a historie běhů. |
| Další akce / Provoz | Jak zjistím technickou příčinu problému? | Běžná cesta k hlavním znalostem či editaci identity. |

Zachovat současnou barevnost, typografický směr a strom crew → agent.
Ukázkové avatary s iniciálami jsou zástupné; není důvod nahrazovat
existující avatary. V produktu převzít aktuální design tokeny a komponenty,
nikoliv CSS této samostatné HTML ukázky. Jazykové názvy při implementaci
sjednotit s lokalizací aplikace; smíšené labely zde ukazují návaznost na
existující Overview/Work/Memory/Edit.

Prázdná data mají jeden klidný stav. Problém má konkrétní důvod a akci.
Rozhodnutí stojí nad běžnou aktivitou, ale historická zpráva ani běžící
úkol nejsou samy o sobě žádostí o schválení.

## Co dodělat — doporučené pořadí

| Pořadí | Dodávka | Kde je práce | Přijetí |
|---|---|---|---|
| 1 | Pravdivé stavy a součty | UI + API | Neúspěšné načtení není 0; historická peer zpráva není blokace; počty respektují celou množinu a období. |
| 1 | Oprávnění osobních profilů | Backend, potom UI | Vlastní data přes self-service; cizí data jen podle explicitní access policy, stejné pravidlo pro read/search/export/delete. |
| 2 | Společný AgentEditor | Převážně UI, sladění kontraktů | Stejné katalogy a validace v Create/Edit; žádná ztracená konfigurace po odstranění Configuration. |
| 2 | Agent/Crew Overview + Work/Team | Převážně UI, doplnění souhrnů | Aktuální práce, výsledek a konverzace mají správný odkaz; žádný druhý zdroj pravdy. |
| 3 | Memory inventory a aktuální obsah | Backend + UI | Obsah lze číst i bez historie; odkrytí dokumentu nevyžaduje spustit agentovu práci. |
| 3 | O mně a personalizace | Existující API + chybějící backendové návaznosti | Zobrazit user model, vyřešit doručení mezi crew a pravidla pro skupinový chat; dokončit nebo pravdivě označit peer-card generátor. |
| 4 | Návrhy znalostí a vazba na skills | Existující API + propojení UI | Jeden návrh, jeden diff, jedno rozhodnutí v Memory i Inbox; odkaz na výsledný skill. |
| 4 | Původ a skutečné použití poznatků | Nová evidence v backendu | UI ukáže zdroj či použití v běhu pouze s doloženým ID/revizí, ne z domněnky. |

Tři malé metriky jsou horní mez výchozího přehledu, nikoliv požadavek
vždy ukázat tři čísla. Wireframe používá dokončené běhy, chyby a evidované
náklady. Při implementaci začít jen metrikami, jejichž scope a úplnost umí
backend doložit. Dokončené běhy nepřejmenovat na „dokončené úkoly“ bez
změny datového zdroje; workspace top-agents není úplná statistika agenta.

## Co zatím nepřidávat

- Volný dashboard builder, desítky widgetů a vlastní graf na každou metriku.
- Skóre inteligence/agenta, odhad ušetřených hodin nebo žebříček podle tokenů.
- Nové kopie Chat, Files, Issues či Routines editorů uvnitř Crews.
- Další přepínač paměti bez vyjasnění vztahu knowledge/recall/Persona/learning.
- Přehled osobních informací o všech lidech jako běžnou součást profilu agenta.

První ucelený release má dodat body 1–3. Body 4 zvyšují dohledatelnost a
hodnotu nad tímto základem. První release nemusí dokončit každý experimentální
memory mechanismus, ale musí umět jasně uvést, který není dostupný.

## Implementační podmínky společného editoru

Wireframe záměrně zkracuje katalogy a ukazuje jen reprezentativní pole.
Produkční editor musí zachovat celý inventář ze současných formulářů:
avatar, role/lead mode, vlastní model, provider/adapter, tool profile,
timeout, prompt/persona, chat suggestions/forms, přístupy, paměť a
existující legacy/feature-gated nastavení. Crew navíc policy, prostředky,
image, síť, integrace a související provozní vazby.

Create/Edit sdílí komponenty, ale POST a PATCH nejsou stejný payload.
Částečné selhání přístupů po vytvoření nesmí při retry vytvořit dalšího
agenta. Edit odesílá změněná pole a zachovává neznámé uložené modely.
Přesun mezi crew vysvětlí změnu děděných přístupů a prostředí. Podrobné
rozdíly a rizika jsou v předchozích auditech.

## Ověření a hranice

Wireframe byl ověřen v Playwright/Chromium: navigace agent/crew/fleet,
Team/Work/Memory, vlastní profil a detail, historie, společný editor,
zachování draftu, save/discard, povinné jméno, crew wizard a návrat,
ukázková šablona, schválení návrhu, prázdný/chybový stav, retry, hledání
a mobilní navigace. Bez uncaught JS errors a bez horizontálního overflow
na šířce 390 px v kontrolovaných hlavních pohledech a editoru.

Obrazové náhledy jsou pořízené ze samostatného souboru s výchozími
modelovými daty. Aplikační kód se nezměnil, nebyl proveden reload Dev2,
nevznikly skutečné crew, agenti, schválení ani modelová volání. Produkční
test/build smyčka není tímto prototypem nahrazena.

Podklady:
[audit Crews & Agents](crews-agents-ux-audit-dev2-2026-09-08.md),
[Memory a personalizace](crews-memory-product-design-dev2-2026-09-08.md).

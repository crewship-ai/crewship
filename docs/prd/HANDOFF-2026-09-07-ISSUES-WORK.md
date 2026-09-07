# Issues: práce lidí a agentů

Implementace #2449 na větvi `feat/issues-work-clarity`, navazuje na první část
#2447 v PR #2448. Výchozí analýza je v
[issues-human-agent-work-contract-2026-09-07.md](issues-human-agent-work-contract-2026-09-07.md).
Tento dokument popisuje skutečně implementované chování, nikoli všechny
rozšiřující scénáře původního návrhu.

## Co se změnilo pro klienta

- Nahoře v detailu je aktuální řešitel, další možné kroky a odděleně člověk
  odpovědný za výsledek. Čekání na vstup odkazuje do Inboxu.
- `Take over` převede práci na přihlášeného člověka. `Hand off` nabídne lidi
  i agenty a vyžaduje předávací poznámku. Lidský výsledek lze odevzdat do review.
- Zadání se neopakuje v záhlaví. Průběh, výsledky a komunikace jsou před
  podpůrnými vazbami; data a technické údaje jsou pod rozbalením.
- U lidského řešitele přestane být dostupné spuštění agenta. Pokud běh ještě
  končí, rozhraní ukazuje převzetí a umožňuje zopakovat zastavení. Předání zpět
  a odevzdání jsou do ukončení běhů zablokované. Předání člověku vytvoří
  právě jednu adresovanou kartu v jeho Inboxu s odkazem na issue.
- Tabule zachovává rozložení. U převzatého úkolu ukazuje aktuálního člověka
  a příznak lidské práce.
- Výsledek rozlišuje technický běh a skutečné outcome. Celý uložený text se
  načítá až při rozbalení; výsledek ze staršího zadání je označen.
- Přílohy lze v detailu přidat a stáhnout. New issue nabízí členy workspace
  a volitelnou šablonu zadání. Na stránce projektu lze vytvářet milníky;
  New project už nevysvětluje uživateli neexistenci API/obrazovky.

## Zápisy a předávání

`issue_work` je trvalý záznam režimu práce, lidského řešitele, poznámky,
revize předání a revize zadání. `missions.owner_user_id` zůstává vlastníkem;
`delegate_agent_id` zůstává agentem. Lidské převzetí delegáta nemaže.

`POST /api/v1/crews/{crewId}/issues/{identifier}/work` přijímá:

```json
{
  "operation_id": "unique-operation-id",
  "revision": 3,
  "action": "handoff_human",
  "target_id": "workspace-user-id",
  "note": "Výsledek je ověřený; prosím potvrď finální verzi."
}
```

Akce: `take_over`, `handoff_human`, `handoff_agent`, `submit`. Ověření revize,
změna práce, komentář, rušení staré práce a receipt v `mission_activity`
proběhnou v jedné transakci. Opakování stejné operace vrátí receipt;
změněný obsah pod stejným ID a zastaralá revize vrátí 409.

Převzetí zastaví nové assignmenty databázovým pravidlem, tedy i zápisy přes
CLI, sidecar a automatické doručení. Čekající doručení předchozího řešitele
jsou superseded. Běžící assignmenty dostanou požadavek na zrušení; dokud
opravdu nedoběhnou, zůstává jejich stav viditelný jako probíhající převzetí.
Rozsah je konkrétní issue a jeho assignmenty, ne nezávislé podúkoly.

Inbox Take over používá stejný transakční helper a vyřeší kartu současně
s převzetím. Rozhodnutí nad stejnou kartou jsou serializovaná před účinky;
`mission_id` má při dohledání přednost před historickým group/chat ID.

Předání agentovi připraví nový krok a ponechá hotové kroky a jejich výsledky.
Spouští se tlačítkem **Start work**. Automatická spolupráce mezi běžícími
agenty dál používá existující komentáře s @mention, session a omezení
hloubky/fan-out; předávací endpoint nepřidává druhou frontu ani nový LLM call.
Agent přes sidecar `/issue/{identifier}/work` může předávat pouze práci, kde
je aktuálním delegátem. Nemůže si přisvojit práci převzatou člověkem.

CLI: `crewship issue work ENG-1 --action take_over`; pro předání doplnit
`--target` a `--note`. Pro opakování přesně stejného zápisu lze zadat
`--operation-id` a `--revision`.

## Výsledky a navazující kroky

Dokončení procesu s outcome NEEDS_HUMAN pozastaví plán na lidský vstup.
PARTIAL/FAILED se nepovažují za dokončenou závislost. Lidská odpověď naváže
nový assignment na čekající krok původního plánu, aby výsledek odpovědi
nezůstal v nesouvisejícím komentářovém běhu. Starý běh zůstává v historii.

Revize zadání se uloží při vytvoření assignmentu. Změna názvu nebo zadání
zvýší revizi; UI upozorní, pokud výsledek vychází ze starší verze. Jde o
upozornění pro review, ne automatické zamítnutí dřívějšího výsledku.

Kontext pro mention/session obsahuje režim práce, revizi, poslední předání
 a omezený seznam příloh. Existující untrusted obálka a tokenový limit zůstávají.
Obsah souborů se nestává systémovou instrukcí.

## Čtení a výkon

Browser načítá posledních nejvýše 100 komentářů; další přes `before_id`.
Kurzorem je stabilní dvojice čas + ID, takže nové zprávy neposunou starší
stránku. Starší CLI čtení bez `page_size` zachovává původní kontrakt.
Události pro otevřený detail se slučují ve 100ms okně. Celé výsledky se
načítají samostatně, nikoli do každé karty běhu.

Převzetí, revize a poznámka jsou součástí workspace backupu. Restore nesmí
ponechat výchozí řádek vytvořený triggerem místo skutečného lidského převzetí;
zároveň nesmí přepsat novější práci již existujícího cílového issue. Historické
výsledky zachovají původní revizi zadání; běhy, které při zálohování ještě
končily pod lidským převzetím, se obnoví jako zrušené.

## Ověření

- Regrese UI: 289 testů Issues a vytvářecích formulářů.
- API: předání, idempotence, souběžná převzetí a odpověď proti převzetí,
  změněné zadání, neplatný příjemce, Inbox takeover,
  pokračování čekajícího kroku a celý výsledek se správnou vazbou na issue.
- Databázový scénář s 10 000 komentáři ověřuje omezené čtení a stabilitu
  kurzoru při nové zprávě; nejde o měření produkčních latencí.
- Outcome tabulka testuje, že neúplný výsledek neodblokuje navazující práci.
- Backup test obnovuje lidské převzetí a chrání novější cílovou práci.
- Chromium fixture používá skutečné komponenty a CSS pro šířky
  390/820/1440 px, ovládání klávesnicí, čitelný výstup a kompletní detail.
  Jde o izolovanou fixture; nezakládá živé klientské úkoly a nespouští LLM.

Finální stav celorepozitářových kontrol a nasazení je uveden v PR #2448.

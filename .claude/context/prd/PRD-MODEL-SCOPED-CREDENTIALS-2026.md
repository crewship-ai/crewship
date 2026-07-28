# PRD — Model-scoped credentials

**Status:** návrh, revize po ověření v kódu · **Datum:** 2026-07-28
**Navazuje na:** `PRD-CREDENTIALS-V2-2026.md` — implementace **až za P3**, důvod v §10
**Cíl v jedné větě:** credential dnes odpovídá „smí tenhle agent mluvit
s Anthropic?"; má odpovídat „smí mluvit s Anthropic **jako claude-haiku-4-5**?"

---

## 1. Proč to stojí za práci

**Nákladová páka je 10×.** Rozptyl uvnitř jednoho providera
(`internal/paymaster/pricing.go`):

| Model | $/M vstup | $/M výstup |
|---|---|---|
| `claude-fable-5` | 10,00 | 50,00 |
| `claude-opus-5` | 5,00 | 25,00 |
| `claude-sonnet-5` | 3,00 | 15,00 |
| `claude-haiku-4-5` | 1,00 | 5,00 |

Jeden agent sáhne po Fable místo Haiku a spálí rozpočet deseti agentů. Dnes
tomu nic nebrání: egress allowlist je doménový (`crews.allowed_domains`,
`internal/egressallow/allowlist.go:168`), takže `api.anthropic.com` je jedno
oprávnění pokrývající všechny modely za ním.

**Je to bezpečnostní kontrola, ne jen nákladová.** Volba modelu určuje
schopnost. Crew na sumarizaci nepotřebuje frontier reasoning model, a omezením
se zmenší, co s předaným klíčem zvládne kompromitovaný nebo prompt-injectnutý
agent. Proto to patří na credential, ne do billing configu.

**Vynucovací bod už existuje.** `reverseProxyToProvider`
(`internal/sidecar/proxy.go`) request už dnes terminuje, čte a injektuje
skutečný klíč.

---

## 2. Rozsah — a poctivá hranice

Vynutit to jde přesně tam, kde už funguje injektáž credentialu, a je to slepé
přesně tam, kde je slepé účtování nákladů. Není to mezera k doplnění později;
je to důsledek vědomého architektonického rozhodnutí a musí se **zdokumentovat,
ne zamluvit**.

| Cesta | Model policy | Proč |
|---|---|---|
| Reverse-proxy, API-key režim — `/v1`, `/openai/v1`, `/gemini` (`proxy.go:592,596,605`) | **vynutitelné** | sidecar request terminuje a vidí do něj |
| CONNECT tunel, OAuth/subscription režim (`proxy.go:467`) | **nevynutitelné** | *„we deliberately do NOT decrypt or inspect the tunnel"* (`proxy.go:492-497`) |

Totéž rozhodnutí o TLS passthrough, které dělá sidecar „structurally blind"
pro náklady (`internal/chatbridge/cost.go:37-47`), ho dělá slepým pro model
policy. UI proto musí u OAuth credentialů říct **„model policy se na tomhle
credentialu nevynucuje"** místo zobrazení allowlistu, který mlčky nic nedělá.
Politika, která vypadá aktivně a není, je horší než žádná.

*(NetBird Agent Network umí vynucovat na každém requestu, protože terminuje
TLS. To je jediný nalezený reálný rozdíl ve schopnostech a jeho převzetí by
znamenalo otočit rozhodnutí o no-MITM — samostatná, větší diskuse.)*

---

## 3. Kde politika žije

**Na credentialu.** `sidecar.Credential` (`internal/sidecar/credstore.go:28`)
už dnes nese per-credential politiku cestující boot payloadem —
`LeaseExpiresAt` ten precedens založil pro #1373, a to s explicitním
odůvodněním, proč politika cestuje s credentialem místo pollování.

```go
type Credential struct {
    ID             string       `json:"id"`
    Provider       ProviderType `json:"provider"`
    Token          string       `json:"token"`
    Priority       int          `json:"priority"`
    LeaseExpiresAt string       `json:"lease_expires_at,omitempty"`

    // AllowedModels omezuje, na které modely smí být credential utracen.
    // Prázdné/chybějící = bez omezení, čímž se zachová chování každého
    // credentialu z doby před existencí tohohle pole.
    AllowedModels []string `json:"allowed_models,omitempty"`
}
```

Zrcadlově v server-side boot builderu na `internal/orchestrator/exec_sidecar.go:686`
(`sidecarCred`), jehož komentář už dnes varuje, že tagy musí sedět.

Proč credential a ne `SidecarNetworkPolicy` (`exec_sidecar.go:663`):

- **Utrácí se klíč.** Dva Anthropic klíče v jedné crew můžou legitimně mít
  různý rozsah — levný na rutinu, neomezený na research.
- **Doručení už funguje**: crew-wide `CredStore`, boot payload, lease sémantika.
- **Precedens**: LiteLLM virtual keys nesou přesně tohle (`models: [...]`
  kontrolované proti modelu z requestu v auth vrstvě) — §8.

Crew-level zúžení, pokud se někdy bude chtít, se protne s listem credentialu:
credential je strop, crew další omezení. Ne v v1.

---

## 4. Extrakce — jedno pravidlo na providera, a tři pasti

Naivní implementace („parsuj JSON, přečti model") selže **fail-open** na třech
z nich.

| Provider | Endpoint | Pravidlo |
|---|---|---|
| Anthropic | `/v1/messages`, `/v1/messages/count_tokens` | JSON tělo, top-level `model` |
| Anthropic batches | `/v1/messages/batches` | **žádný top-level model** — iterovat `requests[]`, číst `.params.model` per položku; jeden batch může míchat modely |
| OpenAI | `/v1/chat/completions` a `/v1/responses` | JSON tělo, top-level `model`. Codex jede Responses defaultně, Chat Completions při `wire_api="chat"` — ošetřit obojí |
| Gemini | `/v1beta/models/<model>:generateContent`, `:streamGenerateContent` | **URL cesta, ne tělo** — podřetězec mezi `/models/` a dalším `:` |
| MiniMax | `/v1/chat/completions` nebo `/anthropic/v1/messages` | přesně podle OpenAI / Anthropic konvence |

**Past 1 — Gemini nemá model v těle vůbec.** Jediný JSON extraktor vrátí
„nenalezeno" pro každé Gemini volání. Když „nenalezeno" znamená povolit, je
Gemini celé bez brány.

**Past 2 — Anthropic batche model vnořují.** Brána kontrolující jen
`body.model` nevidí nic a při fail-open propustí batch, který smí pojmenovat
libovolný model třeba tisíckrát.

**Past 3 — Anthropic fallbacky můžou obsloužit jiný model, než request
jmenuje.** S betou `server-side-fallback-2026-06-01` / `-2026-07-01` může být
request s povoleným modelem obsloužen fallback modelem, když safety
klasifikátory odmítnou. Tělo requestu pořád jmenuje povolený model, takže
inspekce na straně requestu to nechytí.

> **Revize proti původnímu návrhu.** Původní odpověď zněla: u scoped
> credentialu strhnout fallbacky z těla a betu z hlavičky. **To bych neudělal
> jako výchozí chování.** Vyměňuje se tím nákladové riziko za dostupnostní,
> a to druhé se projeví pod zátěží, daleko od konfigurace, která ho způsobila.
> Fallback navíc typicky servíruje **menší/bezpečnější** model, ne dražší —
> takže riziko je malé v obou dimenzích, nákladové i schopnostní.
>
> **Místo toho detekovat.** Odpověď nese skutečně obsloužený model. Pozor ale:
> `copyAndObserveLLM` (`proxy.go:700`) parsuje tělo odpovědi **jen pro
> nestreamované JSON**; SSE jde `io.Copy` skrz a čte se z něj jen kvóta
> z hlaviček. Provoz agentů je převážně streamovaný, takže to není zadarmo —
> u SSE se musí sniffnout první `message_start` event. Pořád levnější než
> odebrat fallback všem. Strip zůstává jako **opt-in** pro credentialy, kde
> na tom někdo trvá.

---

## 5. Kam přijde kontrola

Do `reverseProxyToProvider` (`internal/sidecar/proxy.go`), **před**
`injectCredential`.

Dnešní pořadí (ověřeno): `injectCredential` na `proxy.go:632`, `MaxBytesReader`
až na 641, pak `Clone`. Injektovat první znamená otisknout skutečný klíč do
requestu, který se za chvíli zamítne. Ven ze sidecaru se nedostane, ale pořadí
je špatně a při refaktoru křehké.

```
vyber credential
  ├─ AllowedModels prázdné      → dnešní chování, beze změny
  └─ jinak
       extrahuj model (§4)
         ├─ extrakce selhala    → 403 DENY   (v mezích §6)
         └─ model není v listu  → 403 DENY
inject credential
odešli
```

### Práce s tělem

`http.MaxBytesReader` už aplikovaný je (`proxy.go:641`, `maxRequestBodyBytes`
= 10 MB). Čtení těla kvůli inspekci znamená ho nahradit:

```go
buf, err := io.ReadAll(r.Body)          // už omezeno MaxBytesReaderem
r.Body = io.NopCloser(bytes.NewReader(buf))
r.ContentLength = int64(len(buf))       // chunked příchozí noha se stane Content-Length
r.GetBody = func() (io.ReadCloser, error) {
    return io.NopCloser(bytes.NewReader(buf)), nil   // retry/redirect v transportu
}
```

Dva důsledky k přijetí:

- **Latence — a je to nový náklad.** Tělo requestu se dnes **nebufferuje**;
  `MaxBytesReader` jen omezí, pak se klonuje a streamuje. Streamovaná proxy
  normálně tělo přeposílá, zatímco ho ještě čte; brána, která musí vidět
  `model` před rozhodnutím, tenhle překryv ztrácí. Pořadí klíčů v JSON není
  garantované, takže nakouknout do prvních pár KB není bezpečné.

  Za zmínku v review: repo má na přesně tenhle problém zdokumentovaný postoj
  na straně **odpovědi** — `copyAndObserveLLM` schválně volí `io.TeeReader`
  a odmítá `io.MultiWriter`, protože ten *„would buffer fully before flushing,
  which surfaces as latency to the agent"*. Na requestu ten trik nejde
  (rozhodnout se musí před odesláním); tu asymetrii je lepší pojmenovat
  předem, než se na ni někdo zeptá.

- **Gemini bufferovat nepotřebuje** — model je v cestě. Tam buffer přeskočit.

> **Samostatný podezřelý nález, ověřitelný hned:** 10 MB cap už dnes možná
> ořezává legitimní provoz. Anthropic přijímá až 32 MB na request (base64 PDF,
> obrázky) a agenti přílohy posílají. Tahle featura to nezpůsobila, ale dělá
> z bufferu nosný prvek — a **mění povahu selhání**: dnes se velké tělo pošle
> a odmítne ho provider, po změně ho zamítne vlastní gateway jako fail-closed.
> Stejný výsledek, výrazně horší diagnostika.

---

## 6. Fail closed — ale uvnitř rozhodnuté množiny

Každý nejednoznačný případ zamítá: neparsovatelný JSON, chybějící model, tělo
přes cap, neznámý tvar cesty pro **inferenční** endpoint.

Odpovídá to postoji, který repo pro bezpečnostní kontroly už má —
`leaseEpochSentinel` (`credstore.go:60-65`) argumentuje, že *„for a security
control the safe reading of 'I cannot tell when this expires' is 'it already
did'"*. Táž logika: „nevím, který model to je" se čte jako „ne povolený".

> **Revize proti původnímu návrhu.** Pravidlo *„unrecognised path shape →
> deny"* by rozbilo neinferenční endpointy. Extraction tabulka pokrývá jen
> cesty, které utrácejí tokeny, takže by se zamítlo `GET /v1/models`, Files API
> (`/v1/files`) i vyzvednutí výsledků batche
> (`GET /v1/messages/batches/{id}/results`). Nic z toho model nenese a nic
> z toho nic nespálí — agent s omezeným klíčem by nezjistil ani to, jaké modely
> existují.
>
> **Správné pravidlo: zamítat jen na cestách, které utrácejí.** Explicitní
> seznam inferenčních cest, kde selhání extrakce zamítá; všechno ostatní
> projde beze změny — je stejně chráněné doménovým allowlistem. Fail-closed
> platí **uvnitř** rozhodnuté množiny, ne na její hranici.

Kong AI Gateway drží stejnou linii — tělo s modelem neodpovídajícím modelu
připnutému na routě vrací HTTP 400 místo permisivního parsování.

**Zamítnutí musí být pozorovatelné:** emitovat journal událost ve stylu
`network.egress` s pokusem o model a id credentialu, aby operátor viděl, proč
agent stojí. Tiché 403 je support ticket.

---

## 7. Vynucovat i při konfiguraci, ne jen při requestu

**Tohle je nejdůležitější dodatek k původnímu návrhu a odpovídá jeho Q2.**

Model se agentovi nastavuje (`agent update --llm-model`). Když někdo přiřadí
zakázaný model, každý request skončí 403 a agent je zablokovaný neprůhlednou
chybou daleko od místa, které to způsobilo.

Táž kontrola musí běžet **při přiřazení**, s jasnou hláškou („tenhle credential
je omezený na `claude-haiku-4-5`"). Runtime brána je pak záchytná síť proti
obejití, ne primární UX. Původní Q2 se ptá „tvrdé 403, nebo měkčí selhání" —
odpověď je, že se k tomu rozhodnutí nemá vůbec dojít.

**Tohle je zároveň důvod, proč to nejde stavět před P3** (§10).

---

## 8. Prior art

| Systém | Mechanismus |
|---|---|
| **LiteLLM Proxy** | virtual keys nesou `models: [...]`; proxy parsuje `data["model"]` a kontroluje při validaci requestu, před dispatchem. Nejbližší shoda. |
| **Kong AI Gateway** | model připnutý per route (`config.model.name`); nesouhlas → HTTP 400. Fail-closed postoj k převzetí. |
| **Portkey** | `override_params` / `drop_params` umí model vynutit nebo zablokovat per target; čistý allowlist primitiv nemá. |
| **NetBird Agent Network** | Provider objekt s volitelným allowed-models listem + per-model ceny, vynuceno na L7 proxy terminující TLS. Beta 2026-06. **AGPLv3** (`management/`), takže nevendorovatelné. |
| **Cloudflare AI Gateway** | žádný důkaz o body-level gatingu modelu; routuje podle URL cesty. |

---

## 9. Práce

**Server**
1. Tabulka `credentials`: `allowed_models` (JSON pole, NULL = bez omezení). Migrace.
2. Vystavit v credential CRUD API a `crewship credential` CLI.
3. Naplnit `sidecarCred.AllowedModels` v `exec_sidecar.go:686`.
4. **Validace při přiřazení modelu agentovi (§7)** — nová oproti původnímu návrhu.

**Sidecar**
5. `Credential.AllowedModels` v `credstore.go:28`.
6. `internal/sidecar/modelgate` — extrakce dle §4 plus kontrola allowlistu.
   Čisté funkce, table-driven testy, žádné I/O.
7. Zapojit do `reverseProxyToProvider` před `injectCredential`; buffer dle §5.
8. Journal událost při zamítnutí.

**Testy (napřed, dle pravidla projektu)**
9. Extrakce: případ na providera, plus batch s míchanými modely, plus Gemini cesta.
10. Fail-closed: rozbitý JSON, chybějící model, tělo přes cap.
11. **Neinferenční cesty projdou** (`/v1/models`, `/v1/files`, výsledky batche) — §6.
12. Pořadí: assert, že se na zamítnutém requestu neinjektuje žádný credential.
13. Neomezený credential: bajtově shodné chování s dneškem.
14. Assignment-time validace: zakázaný model odmítnut s čitelnou hláškou (§7).

**Docs**
15. `docs/guides/*.mdx` — pole, a **explicitně**, že se v OAuth režimu nevynucuje.

---

## 10. Proč až za P3

Model-scope se sráží s **P3** z `PRD-CREDENTIALS-V2-2026.md`, které odpojuje
název credentialu od env proměnné a zavádí binding `(scope, slot) → credential`.
Obojí je migrace na tabulku `credentials`.

Vážnější je ale §7: validace při přiřazení potřebuje vědět, **který credential
se agentovi vyhodnotí**. To je přesně to, co P2 (fanout) a P3 (binding) teprve
definují. Psát tu kontrolu dřív znamená psát ji proti resolution cestě, která
se za dva PR změní.

**Zařazení: P9, hned za P8.**

---

## 11. Otevřené otázky

1. **Q1 — přesná shoda, nebo glob?** *Odpověď: přesná po odstranění datového
   suffixu, glob nikdy.* `claude-haiku-*` by automaticky vpustil budoucí
   `claude-haiku-9` za neznámou cenu, a celý smysl allowlistu je, že nový model
   je opt-in. Datovaný snapshot povoleného modelu je tentýž model, takže
   `stripDateSuffix` (`pricing.go`) je bezpečný; hvězdička není.
2. **Q2 — tvrdé 403, nebo měkčí selhání?** *Zodpovězeno §7:* vynutit už při
   přiřazení, runtime 403 je pak záchytná síť, na kterou se běžně nedojde.
3. **Q3 — má ztráta fallbacků být varování při vytváření?** *Zodpovězeno §4:*
   strip přestává být výchozí, takže se ztrácet nemá co. Jako opt-in ano,
   s varováním.
4. **Q4 — postaví se někdy crew-level intersekce?** Otevřené. Má smysl jen
   pokud dvě crews reálně potřebují různé podmnožiny jednoho klíče.

---

## 12. Reference

- Anthropic Messages API — `model` top-level; batche vnořují pod `requests[].params`
- Anthropic model migration / fallbacks — obsloužený model se může lišit od vyžádaného
- Gemini `generateContent` — gramatika cesty `{model=models/*}:generateContent`
- Codex config reference — `wire_api` `responses` vs `chat`
- LiteLLM virtual keys · LiteLLM proxy configs
- Kong `ai-proxy` — fail-closed při nesouladu modelu
- MiniMax Anthropic-compatible API
- NetBird Agent Network — `agent-network/README.md`, blog 2026-06-28 (AGPLv3 server components)

**Neověřeno:** mechanismus tichého downgradu `flash_fallback` byl zmíněn
v dřívější rešerši ke Gemini CLI, ale nepodařilo se ho potvrdit v aktuální
dokumentaci Gemini API ani v `google-genai` SDK. Odpověď nese `modelVersion`
(model, který request skutečně obsloužil) — na potvrzení číst tohle. **Nestavět
logiku na předpokladu, že `flash_fallback` existuje, bez ověření.**

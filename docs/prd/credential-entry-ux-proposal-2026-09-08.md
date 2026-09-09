# Návrh UX vkládání credentials a providerů

Stav: **implementace na dev3** — viz [implementační zpráva](reports/credential-entry-implementation-2026-09-08.md) pro skutečný rozsah, kontrakty a ověření. Níže je původní schválený návrh. Navazuje na cleanup detailu a barevné tagy.
Barvy, ikonky, typografie a komponenty Crewshipu se zachovávají. Mění se
informační struktura, pořadí rozhodnutí a chování při chybách.

Podkladem je aktuální `lib/credentials/item-types.ts`, `login-providers.ts`,
`provider-connection-guides.ts` a `add-credential-wizard.tsx`. Matice providerů
popisuje podporu současného Crewshipu, nikoli všechny možnosti dodavatelů.
Klikací návrh: `/tmp/credential-entry-proposal/index.html`. Ukazuje rozložení
pro všechny typy a větve přihlášení, nikoli implementaci backendu, oprávnění,
validací či přiřazení. Continue v prototypu umožňuje procházet i prázdné formuláře.

## Co zlepšit oproti současnému formuláři

1. Typ nemá být předvybraný. Kliknutí na kartu rovnou otevře jeho formulář;
   první Continue odpadne. Výběr ikony nepatří do prvního rozhodnutí.
2. Název, značka a způsob doručení jsou odlišné údaje. Neopakovat jejich dlouhé
   vysvětlení na každé obrazovce. Ikonu umístit k názvu, proměnnou až k přiřazení.
3. Povinná pole jsou viditelná; skutečně volitelné údaje jsou pod pojmenovaným
   rozbalením. Klíčová pole nesmí zmizet pod obecným Advanced.
4. Uložení, ověření a přiřazení mají vlastní stavy. Zelený výsledek jednoho
   není důkazem ostatních ani kompletního runtime přístupu.
5. Nabízet skutečné úlohy: uložit pro pozdější použití, přiřadit agentovi,
   připojit provider účet, obnovit jeho přihlášení. Jejich dostupnost musí
   odpovídat oprávněním a podporovanému backendu.

## Společná anatomie

Současný CreateSurface: stabilní hlavička, jediná scrollovací oblast a pevná
patička. Desktop zachová přibližně dnešní šířku; mobil stejnou strukturu v
existující mobilní variantě. Žádný nový globální vizuální systém.

- Hlavička: Add secret / Add provider, jedna věta o úloze.
- Kroky: Type → Details → Use, respektive Provider → Connect → Use.
- Od druhého kroku kompaktní řádek vybraného typu nebo providera s ikonou
  a akcí Change. Bez znovu zobrazované celé nabídky typů.
- Patička: Cancel; Back a jedna konkrétní hlavní akce. Na posledním kroku
  Save secret, Save provider nebo Save & assign. Po dokončeném device loginu
  odpovídající Finish setup, protože účet už může existovat.
- Enter nesmí odeslat formulář při psaní víceřádkové hodnoty nebo tagu.
  Zachovat klávesové ovládání, focus trap a existující ochranu rozepsaných změn.
- Chyba patří k poli. Po Continue se označí chybějící pole a fokusuje první;
  obecnou chybu požadavku dát do pevné oblasti nad patičkou. Nečekat s chybějícím
  názvem až na poslední uložení, jak se může stát dnes.

## Add secret: šest formulářů

| Typ | Viditelná základní pole | Volitelné části | Specifické chování |
|---|---|---|---|
| Token | Název, token | Popis, tagy, ikona, doplňková pole | Rozpoznaná značka je návrh. Neznámý token je legitimní vstup. AI klíč může nabídnout přechod do Add provider, ale nesmí být automaticky uložen jako provider účet. |
| Login | Název, username, password | Popis, tagy, ikona | Username před heslem. Zřetelně označit, že username je veřejný identifikátor. Bez příslibu univerzálního otestování libovolného webového loginu. |
| Key pair | Název, access key ID, secret access key | Region, další doplňky | Dva související vstupy vedle sebe na desktopu, pod sebou na mobilu. Nezaměňovat se SSH klíčovým párem. |
| SSH key | Název, nahrání nebo vložení privátního klíče | Passphrase, public key | Zachovat OpenSSH/PEM i víceřádkový obsah. Heslo ke klíči není vždy nutné; dotazovat podle podporované validace. Bez příslibu instalace public key na cílový server. |
| File | Název, soubor nebo jeho vložený obsah | Název souboru, popis, tagy | Vybraný soubor zobrazit jako název + velikost + Replace/Remove; obsah skrytý. JSON kontrolovat jako JSON, ostatní text nesmí být odmítnut jen proto, že JSON není. |
| Certificate | Název, certifikát | Private key, CA chain | Samostatné pojmenované části; nezaměnit certifikát a klíč. Kontrola formátu nepředstavuje ověření TLS spojení ani automatické vydání certifikátu. |

Pro všechny: hodnoty skryté, Show/Hide s jasným názvem; zachovat whitespace,
řádky a uživatelem vložené bajty podle kontraktu daného typu. Volitelné
prázdné části se neposílají jako neplatné prázdné záznamy. Další pole mají
název, hodnotu a explicitní volbu Secret/Public; identifikátory nesmějí získat
přístup ke skrytým hodnotám jen kvůli UX zjednodušení.

Souborový upload je **navrhované doplnění**, současný hlavní flow je založený
na vkládání textu. Při implementaci číst soubor lokálně, respektovat limity API,
rozlišit prázdný soubor, neplatný formát a nepodporovaný binární obsah. Nevydávat
jméno souboru za ověření jeho obsahu. Fingerprinty, odvození public key nebo
kontrolu shody certifikátu s privátním klíčem nabídnout až s ověřeným parserem.

## Add provider: samostatná větev

První obrazovka: hledání a současné značkové karty. Karta říká dostupný způsob
připojení. Kliknutí přejde rovnou na konkrétní účet; žádný další výběr typu secretu.

| Provider v současném Crewshipu | Větve, které návrh zobrazí |
|---|---|
| ChatGPT / OpenAI | Sign in with a code; Import Codex login; API key |
| Claude / Anthropic | Setup token; API key |
| Gemini / Google | Import Gemini login; API key |
| Grok / xAI | API key |
| Cursor | Cursor API key |
| Factory Droid | Factory API key |
| Groq | API key |
| OpenRouter | API key |
| DeepSeek | API key |
| Moonshot / Kimi | Platform API key |
| Z.AI | Standard API key |
| MiniMax | Standard API key |

- Jedna metoda: žádný prázdný přepínač API key/Subscription. Více metod:
  krátké pojmenované volby. Nepodporované předplatné vůbec nenabízet.
- API key: přímý odkaz na jeho získání, jedno tajné pole a krátká informace
  specifická pro poskytovatele. Účet, vlastník a tagy jsou doplňky;
  předvyplněný název lze přepsat.
- Device code: otevřít přihlášení, zkopírovat kód, čekat na potvrzení.
  Zvlášť zobrazit čekání, schválení, vypršení a odmítnutí. Při vypršení
  Generate new code, při problému alternativní podporovaná metoda.
- Import loginu: soubor nebo jeho kompletní obsah. Nikdy nechtít jen část,
  kterou backend nedokáže zpracovat. Chybějící povinnou strukturu vysvětlit
  názvem pole, bez výpisu citlivého dokumentu do chyby.
- Setup token: jeden příkaz s Copy a pole pro výsledek. Nenazývat ho běžným
  API key ani univerzálním OAuth loginem.
- Bez nastavení Keeper tier, pokud je provider ochrana řízená serverovou
  politikou. Rozpoznaný formát neoznačovat jako Verified. Probe nabízí UI
  pouze pro podporované kombinace a příslušná oprávnění.
- Re-login: provider a identita existujícího účtu jsou pevné; obnovuje se
  přihlášení. Nevytvářet omylem další účet a nemařit existující přiřazení.

## Poslední krok: použití a přístup

**Navrhovaná produktová volba, nikoli potvrzení dnešní sémantiky:**

- Save for later — uložit credential bez vytváření nových přiřazení.
- Assign now — vybrat crew nebo jednotlivé agenty, zobrazit ikonky a jména.

Nejdříve ověřit, že všechny backendové cesty umí „uložit bez přiřazení“ skutečně
zaručit; zejména scope/crew dědičnost nesmí způsobit nečekanou distribuci.
Scope viditelnosti, explicitní granty a delivery bindings nesmějí být sloučeny
do jedné nepravdivé věty „všichni agenti to dostanou“.

Pokud se přiřazuje: pro textové hodnoty nabídnout podporovanou proměnnou,
pro soubory jen doručení, které skutečný runtime umí. Pokročilé názvy/path
jsou upravitelné pouze tam, kde je backend přijímá a validuje. Zobrazit konflikt
obsazené proměnné dříve než po vytvoření, pokud je dostupná odpovídající kontrola;
žádné tiché přepsání existující vazby. Provider účet má své vlastní přiřazení
placení/provozu modelu, ne obecné pole pro libovolnou env proměnnou.

Advanced obsahuje pouze platná, editovatelná nastavení: scope, skutečnou Keeper
politiku obyčejného secretu a uživatelskou expiraci/reminder. Expiraci dodanou
providerem nevydávat za volně editovatelnou. Vlastník účtu je odlišný od agenta,
který ho používá.

Po dokončení otevřít detail s pravdivým výsledkem: Saved; Connection not tested /
Verified / Check failed; Assigned / Not assigned / Assignment incomplete.
Žádné zelené Ready odvozené pouze z uložení, typu, ikony nebo tool inventory.

## Chyby, návraty a lifecycle

| Situace | Chování |
|---|---|
| Back | Zachovat návrh v paměti; po návratu obnovit vstupy. |
| Změna typu / providera / metody | Nechat metadata, přenositelné údaje zachovat; před zahozením nekompatibilního tajemství upozornit. Stejná volba nesmí mazat vstup. |
| Zavření rozepsaného formuláře | Stávající discard guard. Po potvrzeném zavření odstranit tajné hodnoty z paměti; nepersistovat je do localStorage. |
| Změna workspace | Rozepsané hodnoty nesmějí být odeslány do jiného workspace. Vázat formulář a probíhající požadavky k původnímu cíli. |
| Špatný vstup | Chyba u pole, zachovaný vstup a cesta k opravě. Žádné neověřené opravování tajných bajtů. |
| Duplicitní název | Zachovat celý návrh, opravit jen název. Existující credential nepřepsat. |
| Síťová chyba při uložení | Nevydávat nejistý výsledek za definitivní selhání; pro bezpečné opakování potřebujeme idempotenci nebo spolehlivé dohledání výsledku. |
| Credential uložen, další pole selhala | Zobrazit, co existuje a co chybí. Retry doplňkových polí, nikoli další POST vytvoření credentialu. |
| Credential uložen, přiřazení selhalo | Otevřít uložený detail s opravou přiřazení. Nevytvářet duplicitu. |
| Device login už vytvořil účet, uživatel zavře wizard | Vysvětlit, že účet existuje, a nabídnout pokračování nastavení; netvrdit, že Cancel vrací celý flow zpět. |
| Nedostatečné oprávnění | Nenabízet nepovolený flow ani nepovolené přiřazení. Oprávnění dál ověřuje backend. |
| Obnova / výměna hodnoty | Samostatná cílená akce. Typ a metadata se nemění; uložit hodnotu vydanou poskytovatelem, neslibovat upstream rotaci. |
| Edit | Stejné skupiny a pořadí jako create; uložená hodnota se nikdy nepředvyplní. Výměna explicitně volitelná. |

## Implementační rozdělení

1. **UI nad existujícím chováním:** zkrácený výběr typu, přeuspořádané formuláře,
   povinná pole validovaná už v Details, konzistentní ikona u názvu, provider
   větvení a konkrétní chyby. Zachovat existující barevný a komponentový systém.
2. **Doplnění vstupů:** souborový vstup a bezpečné lokální kontroly formátu;
   porovnat je s backendovými limity a serializací všech tvarů.
3. **Backendově podmíněné UX:** přiřazení přímo z wizardu, garantované uložení bez
   přiřazení, validace konfliktních bindings a spolehlivé opakování částečně
   dokončeného save. Nabízet až po ověření nebo doplnění těchto kontraktů.
4. **Ověření:** všech šest secretů, všech 12 providerů a jejich dostupné metody,
   role/capability varianty, mobil, klávesnice, Back/Cancel, změna workspace,
   expired device code, neplatné soubory a částečné zápisy. Testovat odlišnost
   Saved/Verified/Assigned. Nepoužívat skutečná tajemství pro UI fixtures.

Starší API typy bez samostatné create karty (např. ENDPOINT_URL, OAUTH2) se při
editaci nesmějí převést na Token jen kvůli jednoduššímu UI. Jejich další create
flow je samostatné rozhodnutí až podle podporovaných backendových kontraktů.

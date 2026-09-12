# Routines — opravy po oponentuře 12. září 2026

Oponentura ověřila dev1 `7d470200a` a přijala konkrétní důkazy N1, N3 a N4,
ale odmítla uzavření celého technického zadání. Předchozí závěr byl příliš široký.
Lidská brána §11 zůstává otevřená. Tento dokument doplňuje historii, nemaže ji.

## Nálezy a implementace

| Nález | Změna | Ověření / stav |
|---|---|---|
| Rollback mimo bránu presetů | Kontrola kompatibility a update HEAD v jedné transakci, API vrací 409 s identitou plánu | `TestSchedulePresetGate_RollbackDoor`: před opravou 200, po opravě 409; HEAD nezměněn; po vypnutí plánu rollback projde |
| Zapnutí neplatného plánu | Přechod disabled→enabled kontroluje výsledný cíl, pin, vstupy i wake preset; hint vyžaduje opravu před zapnutím | `TestPresetValidation_ReenableRequiresCompatiblePreset`: před opravou 200, po opravě 400; oprava+enable 200. Wake kontroluje vlastní test |
| Deadline row/journal | Vypršení deadline není zrušení; obě vrstvy FAILED | Rozšířený `TestClassify_RunDeadlineExceededStaysFailed` před opravou padá na journal CANCELLED, po opravě prochází |
| Publish bez potvrzení | Povinný náhled změn → Confirm and publish; návrat zachová text | Test namountovaného `RoutineCreateDialog` vyžaduje nulové test_run/publish requesty před potvrzením |
| Živé editory v draftu | Plány, webhooky, rozpočet a přístupy v Plan mimo Edit | Test editoru ověřuje nepřítomnost živých editorů; plánování nové rutiny zůstává částí jejího publikovaného draftu |
| Neoznačené zóny / číselná data | Společný anglický formát s názvem zóny; plán používá svou zónu, kalendář označuje browser-local zónu | Test letního/zimního času Europe/Prague a UTC |
| Run neuvádí účinky | Potvrzení i bez vstupů; agenti, hosty a typy credentials; explicitní hranice dynamických účinků | Test formuláře a vnořených akcí; URL hesla/query ani hodnoty credentials se nevypisují |
| Inbox Retry a chat slash | `Prefer: respond-async` na skutečných routine run požadavcích | Testy obou klientských cest kontrolují hlavičku |
| Nepřesná dokumentace | PRD úvod, P8b, CLI, work order, review audit | P8b má samostatný JSON s identitou původního nasazení; neprokazuje dnešní opravy |

Dodatečná kontrola našla další tři mezery: samostatné Activate draftového
plánu, zastaralý výsledek API preflightu při souběžné publikaci a změnu receptu
wake rutiny. Všechny tři mají červenou reprodukci. Veřejné zápisy plánu
a Activate nyní ověřují výsledný preset v transakci zápisu; publikace i rollback
posuzují také wake presety povolených plánů. Vypnutí a nesouvisející úpravy
existujícího plánu zůstávají možné.

## Kód, který skutečně obsluhuje UI

Editor: `routine-create-dialog.tsx`, detail: `routine-card-detail.tsx`,
spuštění: `routines-detail-panel.tsx` → `routine-run-inputs-dialog.tsx`,
opakování: `routine-run-detail.tsx`. Odstraněné `routine-overview-tab.tsx`
a `routine-editor-tab.tsx` neměly produkční importy. Byly odstraněny také jejich
samostatné testové soubory a nepoužívané mocky; testy živého editoru zůstaly.

## Přesnost starších tvrzení

- P8 prokazuje `f7a43cd22`. [P8b](routines-p8b-2026-09-11.json) doplňuje
  skutečné opakování na `7d470200a` z 11. září 18:03–18:06 UTC.
- „Journal 500“ v P8/P8b znamená stránku `/journal`, ne journal detailu běhu.
- Bez skutečného strojového review byly #2497, #2498, #2501 a #2507.
  U #2494 nebyla pokryta koncová hlava. #2507 retrigger nebyl odeslán.
  Vlastní red→green kontrola není nezávislé review.
- Oponentovi prošlo 133 opakování rodiče testu záloh. Historické lokální
  selhání není univerzální reprodukce; cílená oprava sondy a mutační kontrola
  jsou podložené. Produkční zálohování tato změna neopravovala.
- Přijetí zrušení N1 se nevztahovalo na vlastní deadline běhu. Tento samostatný
  případ je nyní kontrolován zároveň v řádku i journalu.

## Stav ověření

[Veřejný protokol konečného ověření](https://github.com/crewship-ai/crewship/issues/2473#issuecomment-5646862404)
uvádí aktuální head, merge, výsledek review a přesnou identitu nasazení.
[PR #2514](https://github.com/crewship-ai/crewship/pull/2514) je zdrojem stavu CI
a merge. Následující odstavec je snímek před mergem, nikoli pohyblivé tvrzení
o tom, co právě běží na dev1.

Cílené Go testy API/pipeline prošly po třech doložených červených reprodukcích.
Také vlastní živé HTTP sondy na nezměněném dev1 `7d470200a` reprodukovaly
rollback 200 a re-enable 200 se změnou dat. Vlastní rutina a plán byly uklizené.
Frontend: 52 souborů, 337 testů prošlo; dalších 39 testů pokrývá finální
doplnění načtení baseline existujícího draftu z chatu. Lint, produkční build
a `go vet ./...` prošly. První hlava PR měla chybu inicializace proměnné v tomto
doplnění; lokální testy i CI ji zachytily a navazující commit ji opravil.
Browser nad lokálním exportem a dev1 API potvrdil draft bez živých editorů,
povinné potvrzení, návrat se zachovaným textem, Publish 201 s toastem,
čas plánu v Europe/Prague, potvrzení Run i bez vstupů a 390 px bez overflow.
To není důkaz nasazení nového backendu. Úplná Go sada, konečný review,
merge a nasazení následných oprav nejsou tímto zápisem prohlášené za hotové.
Výsledky následujících kontrol a nasazení se zaznamenávají do veřejného
protokolu výše; jeho identity nelze nahrazovat staršími P8/P8b.

Soukromé pracovní logy: `/srv/crewship/backups/crewship_1/routines-opponent-20260912/`.
Bez změn cizích rutin a workspace; WIP v hlavním checkoutu je zachován.

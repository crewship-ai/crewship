# Iterace 10a: bezpečný rozepsaný chatový handoff

Větev `feat/chat-draft-handoff`, issue #2668, založená z `origin/main`
`42b4b9ac8` (2026-09-23). Samostatný předpoklad pro „Ask about this Page“.

`/chat/<agent>?new=1` vybírá výslovně novou lokální session i tehdy, když
agent už má historii nebo URL obsahuje staré `session`. Serverový řádek
vznikne až při odeslání. `?draft=1&prompt=...` vloží text do composeru bez
automatického odeslání. Hodnota handoffu se čte jednou a připne k první
vybrané session; přepnutí do jiné session ji nepřenese. Legacy `?prompt=` bez
`draft=1` dál automaticky odešle jednou. Composer upřednostní neprázdný
uložený nebo právě psaný draft před příchozím prefill textem.

Tato část zatím nepředává Page data. Její další krok musí jednorázově přečíst
`?page=<slug>` před tím, než výběr session přepíše URL, ověřit lidský i
agentní přístup a zobrazit odstranitelný kontext. Citlivý obsah do query
parametrů nepatří.

Testy pokrývají explicitní novou session i při chybě načtení staré historie,
konflikt se starým `session`, žádný POST/auto-send, přepnutí konverzace,
původní auto-send kontrakt a zachování uloženého i právě psaného draftu.
Cílených 14 testů a úplná frontend sada (`775` souborů, `9264` testů) prošly.
TypeScript, lint (0 chyb), statický build, Go vet, docs-surface-check,
docs-inventory strict a agents-invariants jsou zelené. Go produktový kód se
neměnil; úplnou Go sadu tato větev neopakovala.

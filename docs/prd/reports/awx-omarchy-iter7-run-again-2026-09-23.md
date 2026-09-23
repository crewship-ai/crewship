# Iterace 7 — Run again s ověřenou verzí

Implementace navazuje na #2661 a #2663; issue #2664. Při otevření formuláře detail rutiny načte aktuální katalog i archivovaný HEAD a porovná jejich hash. Formulář ukáže číslo této verze a její vstupy. Zvolená historická verze se načítá přímo z archivu. Chybějící hash nebo rozpor katalogu s archivem znamená chybu, nikoli spuštění podle odhadu.

POST `/run` přijímá volitelný `expected_definition_hash`. U aktuálního receptu porovná HEAD, vrátí 409 při změně a přeloží schválený snapshot na `pinned_version`. Executor načte tuto neměnnou verzi z archivu; nemůže po kontrole znovu vybrat nový HEAD. U explicitně zvolené historické verze se hash porovná s archivem. Volitelný hash je podporován jen pro okamžité spuštění; odložené starty mají vlastní již existující politiku pinu. Stejný idempotency klíč po nejisté odpovědi vrátí původní run ID i tehdy, když mezitím HEAD postoupil; nový klíč dostane 409.

Při ověřeném spuštění se přeskočí živé step overrides, protože mění účinný recept bez změny jeho definice/hash. Běžné starty bez očekávaného hashe fungují jako dřív. Autorizace, governance, input a credential/integration/resource gates zůstávají na svých místech; executor je znovu provádí. Formulář otevřeně říká, že vstupy se ukládají doslova a nový běh může opakovat účinky. CLI nabízí `--version` jako alias pinu a `--expected-hash`; OpenAPI a CLI docs jsou doplněné.

Ověření: test aktuálního/stale hashe, 409 bez běhu, verze/hash skutečně uloženého běhu, historický pin, idempotentní retry po publikaci, odmítnuté odložené spuštění, runtime override skip, UI načtení katalog+archiv, 409 a dostupné opětovné načtení. Existující testy kryjí historické 404/422 a governance. Plné testy/CI doplnit po doběhnutí.

Limit: pin reprodukuje autorizovaný recept, nikoli externí svět, stav secretů či účinky dřívějšího běhu. Živý průchod jiným účtem ještě není součástí tohoto PR. Iterace 8–10 a celková akceptace zůstávají otevřené.

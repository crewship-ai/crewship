# Codex — Crewship

## Co je Crewship

Platforma pro týmy AI agentů na vlastním serveru. Go server obsahuje webové UI
(Next.js); každý tým má kontejner. Práci spouští fronta, plánovač a události.

## Kde pracuješ

Na serveru jsou tři samostatné běžící vývojové instance:

| Adresář | API | Web |
|---|---|---|
| `/srv/crewship/crewship_1` | `8081` | `3011` |
| `/srv/crewship/crewship_2` | `8082` | `3012` |
| `/srv/crewship/crewship_3` | `8083` | `3013` |

Konkrétní vzdálené adresy vývojových instancí a soukromých Git serverů patří
do lokální konfigurace nebo soukromého kontextu, ne do veřejných instrukcí.

- Na začátku ověř `pwd`, `git status --short` a `./dev.sh status`.
- Pracuj pouze s instancí odpovídající aktuálnímu adresáři nebo zadání uživatele.
  Vedlejší klony mohou mít rozpracované změny jiných relací; zachovej je.
- CLI pro instanci N: `/tmp/crewship-N-dev --server http://localhost:808N`.
  Nahraď N číslem 1, 2 nebo 3; před změnou ověř cíl a nápovědu příkazu.
- Běžnou správu dělej přes CLI, ne přímým zápisem do databáze.
- Při potřebném nasazení změn: `sudo systemctl reload crewship-ws@N`.
  Jde o restart živé instance. Stage (`8084`) spravuje CD, nenasazuj tam ručně.
- `./dev.sh seed` vytvoří celé demo, nejen účet `demo@crewship.ai`.
  Definice je v `cmd/crewship/cmd_seed.go`. Reset není způsob přidání uživatele.

## Jak pracovat

- Odpovídej stručně, v jazyce uživatele. Dodrž požadovaný rozsah práce.
- Pravidla vývoje, claimů, testů a review jsou v [AGENTS.md](AGENTS.md)
  a [CONTRIBUTING.md](CONTRIBUTING.md); neopisuj je sem.
- Produkt: [README.md](README.md). Mapa dokumentace: [docs/README.md](docs/README.md)
  (kontrakty `docs/specs/`, rozhodnutí `docs/decisions/`, runbooky
  `docs/runbooks/`, dosavadní veřejné návrhy `docs/prd/` se stavovým indexem).
  Infrastruktura: `~/crewship-infra`.
- Soukromý pracovní kontext zjisti přes `git config --get crewship.internalContext`.
  Je-li dostupný, přečti jeho `AGENTS.md` a příslušný rozcestník. Nové interní
  analýzy, předání a experimenty ukládej tam. Hranice veřejné dokumentace:
  [private-context.md](docs/development/private-context.md).
- Ověř výsledek a rozlišuj, co říká dokumentace a co bylo skutečně vyzkoušeno.
- Sem patří pouze stručné, dlouhodobě užitečné informace společné všem třem
  instancím. Podrobné interní předání patří do soukromého kontextu; hesla a tokeny sem nepatří.

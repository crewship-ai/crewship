# Technické ověření: vynucení izolace běhů uvnitř crew

Stav: zadání, neprovedeno. Navazuje na [rozhodovací PRD](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md). E0 je povinný rozsah 1.0; toto ověření rozhoduje o navazujícím E1, nikoli o možnosti E0 vynechat. E1 není slíben jako malá změna ani jako část vydání.

## Otázka

Lze uvnitř stávající crew infrastruktury vynutit izolaci souborů a podporovaných operací mezi běhy bez nepřijatelné změny image, providerů a sidecar autorizačního modelu? Porovnat odlišné UID s omezenými adresáři a případným mount namespace proti přidání samostatného sandbox lifecycle E2.

První fáze je návrhová mapa a threat model. Experiment probíhá jen v odděleném prostředí. Stávající kontrakt AGENTS.md zachovává agent UID 1001 a sidecar UID 1002; tento dokument ho nemění. Produkční zavedení rozsahu principals vyžaduje explicitní aktualizaci tohoto kontraktu a odpovídajících kontrol.

## Povinná mapa dopadů

- Vytvoření uživatele v devcontainer feature/image; podporované BYOI images a platformy.
- Všechny terminal/provider exec cesty, zejména hardcoded UID/GID, chown a supplementary groups.
- Writable HOME, `/output`, `/tmp`, secrets, credential refresh vlastnictví a persistent volume permissions.
- Tmux server/socket, attach/cancel, signály, reap, UID reuse a proces zbylý po konci lease.
- Sidecar na sdíleném localhost: identita runu, write capability, memory scope, credential attribution a revokace. Filesystem UID sám neautorizuje HTTP požadavek.
- Overlay/CoW příprava: požadované privileges, hostový helper, mount namespace, readonly podklady a cleanup. Nepřidávat workerům privilegium mountovat jako zkratku.

## Experiment a úspěch

Dva mock workery A/B mají oddělené principals a soukromé cesty. B nesmí číst tajný soubor A, přepsat jeho HOME/output, odstranit jeho auth, signalizovat jeho proces ani použít jeho sidecar capability. Ověřit přímé cesty, symlinky, sdílené skupiny a starý proces po opětovném přidělení UID. Oba musejí dostat vlastní funkční terminal attach a stream; cancel B zachová A.

Read-only společná paměť nesmí být přímo přepsatelná a podporovaný memory write musí ověřit run capability a aktuální generation. Shared localhost či společný kernel nepovažovat bez analýzy za vyřešenou ani automaticky neexistující hranici; popsat přesný threat model a nechráněné zdroje.

Restart, ztráta lease a cleanup nesmějí opustit platné credentials nebo přidělit stejný principal novému běhu, zatímco starý může stále zapisovat. Změřit režii přípravy, cleanup a storage; uvést skutečný filesystem a dostupnost CoW, nepřenášet cizí benchmark.

Výstup: matice dotčených komponent, testovací důkazy, změny autorizačního kontraktu, platformní omezení a ADR `E1 implementovat / preferovat E2 / další důkazy`. Úspěchem není jen `permission denied` při jednom přístupu do `0700` adresáře. Neúspěch E1 nemění release E0, ale zakazuje označit E0 za vynucenou izolaci.

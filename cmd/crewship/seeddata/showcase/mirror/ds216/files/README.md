# Template Crewship Design System

Testovací design systém pro Crewship: noční moře (`deep`, `deck`) a jedna signální oranžová (`signal`). Technický, klidný, s jedním akcentem na obrazovku.

## Obsah a tón

- Píšeme krátce a věcně, česky, v druhé osobě: „Přepni crew“, ne „Uživatel může přepnout crew“.
- Čísla a stavy vždy slovem i barvou: „Hotovo“, „+7 proti minulému týdnu“.
- Žádné emoji, žádné vykřičníky.

## Vizuální základy

- **Barvy:** pozadí `surface`, karty `panel` s okrajem `line`. Text `ink`, vedlejší `ink-muted`. Akcent `signal` jen na jednu věc na obrazovce (primární akce nebo nejdůležitější číslo); text na něm je `ink-on-signal`.
- **Písmo:** IBM Plex Sans na nadpisy i text (Google Fonts), JetBrains Mono na CLI příkazy, čísla v metadatech a eyebrow štítky.
- **Tvary:** karty `radius-lg`, tlačítka `radius-md`. Okraje místo stínů.
- **Rozestupy:** krok 4 px; v praxi `space-2` až `space-6`.

## Ikonografie

Lineární ikony, tah 2 px, kulaté konce, barva textu. Zatím žádná vlastní sada ani logo: název píšeme písmem IBM Plex Sans 700.

## Co zatím chybí

Komponenty, logo a vlastní ikony. Tento systém je malý první návrh postavený bez zdrojových podkladů.

# Omarchy a Crewship: pracovní prostředí pro lidi a agenty

> Aktualizace 2026-09-14: společné produktové priority a rozsah AWX + Omarchy jsou v [navazujícím PRD](awx-omarchy-product-improvements-2026-09-14.md). Desktopové varianty tohoto výzkumu zůstávají pozdějším směrem; pro release 1.0 mají přednost levná propojení současných funkcí.

**Doporučení: rozvíjet integraci pracovního prostředí do Crewshipu, ale nerozhodovat nyní o plošném přechodu na Omarchy ani o vlastním produktu pro kompletní firemní desktopy.** Nejlepší první investice je konektor mezi desktopem a Crewshipem, následovaný browserem či desktopem přidělovaným na konkrétní úlohu. Plné Omarchy je vhodný experimentální profil pro VM. Pro kancelářské uživatele musí obhájit své ovládání a kompatibilitu proti jednoduššímu desktopu nebo jejich současnému počítači.

Omarchy má doložitelně dobrou integraci příkazů, aplikací, témat, diagnostiky a agentů. Jeho přínos spočívá v připraveném a přizpůsobitelném prostředí. Crewship může dodat trvalou identitu agentky, firemní kontext, řízení práce, rozpočet, oprávnění a audit. Tyto vlastnosti samotným nasazením linuxové distribuce nevzniknou.

Odhad rozvoje od malého ověření po spravovanou firemní službu je **10–15 člověkodnů pro rozhodovací pilot, 55–90 člověkodnů kumulativně pro omezený produkt s agentním desktopem a 120–220 člověkodnů kumulativně pro firemní desktopovou službu**. Při modelové ceně práce 10–15 tisíc Kč za člověkoden jde o 100–225 tisíc Kč, 550 tisíc–1,35 milionu Kč a 1,2–3,3 milionu Kč. Jde o odhady rozsahu, nikoli nabídku či naměřenou produktivitu.

Model 10 a 50 firemních uživatelů vychází přibližně na **1 566 a 1 037 Kč měsíčně na uživatele za provoz desktopu**, včetně základní podpory a provozní rezervy, ale před AI, rozpočítáním vývoje, průběžnou údržbou produktu, licencemi aplikací a marží. Výsledek silně závisí na souběžnosti, hustotě relací a podpoře. Počet desktopů na hostitele je zatím plánovací předpoklad.

## 1. Tři různé produkty

### Osobní asistentka napojená na existující počítač

Člověk dál používá svůj současný systém. Crewship zná jeho práci a agentku, desktopový konektor umí předat vybraný soubor, odkaz či screenshot a zobrazit výsledek nebo čekající schválení. Agentka provádí úlohy na serveru; přístup k lokálnímu počítači je explicitní a omezený.

Tento produkt nejlépe ověří, zda zákazník platí za návaznost práce a pomoc agentky. Nevyžaduje migraci aplikací, periferií ani pracovních návyků. Omarchy může dostat obzvlášť dobře integrovaný panel, ale základní služba by neměla záviset na jediné distribuci.

### Vlastní počítač pro agentku

Agentka dostane izolovaný browser nebo desktop, ve kterém obsluhuje konkrétní aplikace. Člověk může sledovat práci a dočasně převzít řízení. Typické zadání: připravit podklady v systému bez vhodného API, ověřit vzhled dokumentu nebo reprodukovat chybu v aplikaci.

Prostředí se pronajímá na úlohu nebo identitu. Trvalé soubory a paměť přežijí jeho vypnutí. Výchozím prostředkem pro běžné úlohy zůstávají API, shell a strukturované nástroje. Vizuální ovládání přichází tam, kde přináší skutečný přístup k jinak nedostupné práci.

### Firemní vzdálený desktop pro člověka s agentkou

Zaměstnankyně se z levného notebooku připojí ke svému serverovému počítači. Očekává každodenní spolehlivost, zachování dokumentů, tisk, českou klávesnici, přihlášení, zvuk, schůzky a pomoc při problému. Agentka pracuje vedle ní nebo v oddělené relaci.

To je největší produktový závazek. Výpadek už nezastaví jen automatizaci; může zastavit celou práci zákazníka. Vývoj vzdáleného desktopu a provoz poskytovatele spravovaných pracovních stanic jsou dvě rozdílné nákladové položky. Označení „Martička“ může patřit člověku i agentce; architektura musí vždy rozlišovat jejich identity a oprávnění.

## 2. Co potvrdil rozbor zdrojového kódu

Referenční stabilní vydání je **v4.0.3**, publikované 8. září 2026, commit `0534987009061cbe2dacdde4ad564092ab698d12`. Doplňkově byl prohlédnut vývojový strom `b5589faaf80c6f87c07d4560fca37c4a81722f28`. Není správné odvozovat vydání pouze ze souboru `version`: v prohlédnutém vývojovém stromu stále obsahoval `4.0.0.alpha`. Rozhodující jsou tag, balíčky a jejich konkrétní revize.[^release]

### Příkazy jako společný jazyk člověka a agenta

V referenčním tagu je 444 souborů pod `bin/`. To není počet nezávislých produktových funkcí, ale ukazuje rozsah integrační vrstvy. Dispatcher `bin/omarchy` zpracovává metadata příkazů: popis, argumenty, příklady, aliasy i informaci o potřebě sudo. Metadata vznikají přímo u implementace.[^commands]

Pro Crewship je cenný princip: jedna popsaná operace může být dostupná z menu, CLI i nástroje pro agenta. U nás by kontrakt navíc měl obsahovat typované argumenty, identitu, oprávnění, strojově čitelný výsledek a auditní událost. Přebírat konkrétní shellové příkazy jako libovolný vzdálený administrátorský vstup by bylo nevhodné.

### Integrace agentů je skutečná, ale je to integrační vrstva

`omarchy-agent-prompt` předává prompt launcheru `omarchy-agent`. Ten vybírá konkrétní CLI, skládá jeho argumenty a otevírá terminál nebo běží inline. Ve v4.0.3 se například Claude spouští s `--permission-mode auto`; jiné adaptéry používají vlastní režimy automatického schvalování. Nejde o jednotný nový agentní engine.[^agent]

Vydání 4.0.3 přidává také integraci OpenClaw a Hermes včetně jejich desktopového či terminálového použití. Je to důkaz, že Omarchy umí dobře zasadit další agentní produkt do svého prostředí. Není to důkaz společné firemní paměti nebo společného řízení všech agentů.[^release]

Pro řízené úlohy musí zůstat vlastníkem běhu Crewship. Lokální agent spuštěný klávesovou zkratkou jinak snadno vytvoří druhou větev historie, spotřeby a oprávnění. Nabídka „Pokračovat s Martičkou“ by měla navázat na existující identitu a úlohu v Crewshipu, nikoliv spustit nezávislý chat.

### Diagnostika jako dobře ohraničená agentní úloha

`omarchy-agent-crash` sestaví kontext z PID, programu, signálu a času a předá jej diagnostické skill. Skill vyžaduje důkazy, kontrolu nedostatku prostředků a korelaci časové osy. Výslovně odděluje diagnózu od oprav a upozorňuje, že core dump může obsahovat citlivá data.[^crash]

To je silný vzor pro Crewship: chyba běhu nebo služby → omezený diagnostický balíček → analýza → návrh změny → ověření. Automatická „samoléčba“ bez odděleného oprávnění k opravě by byla podstatně rizikovější. Plné dumpy by se neměly standardně posílat modelu ani ukládat do běžné historie.

### Jeden shell a rozšiřitelné panely

Bar, menu, notifikace a další povrchy používají společný Quickshell host a manifesty pluginů. Některé panely se načítají až podle potřeby; služby či panely s `keepLoaded` zůstávají načtené. Menu je deklarované v JSONC a jeho akce spouštějí příkazy. To vysvětluje soudržnost a přizpůsobitelnost prostředí.[^plugins]

Výhoda společného procesu má i cenu: chyba nebo nepřiměřená spotřeba pluginu může ovlivnit větší část desktopu. Pro Crewship bych převzal katalog schopností, konzistentní panely a načítání podle potřeby. Backendové operace a nedůvěryhodný kód musí mít samostatné hranice.

### Metriky AI oddělené od zobrazení

Panel agentů čte JSON záznamy o spotřebě. Jednotlivé collectory zajišťují data, samotný panel je prezentační vrstva. README popisuje odlišné zdroje informací a rozlišuje odhad zůstatku od autoritativní hodnoty.[^usage]

Crewship může dodat obdobný panel pro běžící práci, čekající vstup a zůstatek rozpočtu. Údaje by měly pocházet z jeho vlastního journalu a paymasteru, s jasným označením případné neúplnosti. Nekopíroval bych desktopové parsování transcriptů jako hlavní způsob firemního účtování.

### Webové aplikace a témata

`omarchy-webapp-install` vytváří desktopový launcher pro webovou aplikaci, zpracovává ikony a validuje URL i serializaci desktopového souboru. `omarchy-theme-set` koordinuje změnu tématu napříč podporovanými aplikacemi.[^webapps]

Pro Crewship je přímo přenositelné snadné zpřístupnění firemních aplikací: CRM, dokumenty a interní Pages se objeví v pracovním prostředí podle role. Samotný launcher ale aplikaci neintegruje datově a neodděluje její oprávnění. Firemní katalog musí být navázán na identitu a povolené cíle.

### Aktualizace a obnova

Aktualizační řetězec má zámek, kontrolu místa, snapshot, aktualizaci balíčků a následné migrace. V prohlédnutém kódu může po selhání snapshotu pokračovat s výstrahou. Migrační marker se zapisuje po úspěšném provedení skriptu; to samo nedělá z více změn v celém OS atomickou transakci.[^updates-code]

Pro fleet je lepší převzít koordinaci změn, ale zavádět je přes otestovaný obraz, malou ověřovací skupinu a řízený postup. Obnova systému nesmí vrátit journal provedených externích akcí zpět a způsobit opětovné odeslání či objednání. Snapshot OS, záloha dokumentů a evidence úloh mají odlišnou sémantiku.

### Obnova shellu a testy

`omarchy-launch-shell` uchovává logy v journalu a omezuje opakované restarty. `omarchy-restart-shell` řeší i zamčenou relaci a ověřuje její opětovné zabezpečení. Z těchto detailů je patrné, že provozní chování není ponechané pouze na ručním restartu.[^shell]

V tagu je 296 souborů pod `test/`, včetně základů a fixture; nejde o 296 ověřených testovacích případů. `test/all` spouští CLI a shell suite, nikoliv automaticky celou akceptaci živého desktopu. Ta má samostatný runner. Existence testů neprokazuje zelený CI stav konkrétní kombinace balíčků.[^tests]

## 3. VM je lepší začátek než port kompletního OS do Dockeru

Repozitář `omarchy-iso` má automatickou instalaci z připojeného média `cidata`, příklad pro Proxmox a testovací harness, který ovládá skutečný systém v QEMU pomocí QMP, screenshotů, OCR a virtuálních kláves. Prohlédnutý harness používá KVM, virtuální grafickou kartu a standardně 8 GiB RAM. To je konkrétní technická opora pro VM prototyp.[^iso][^iso-code]

`cidata` zde neznamená automatickou kompatibilitu se všemi běžnými cloud-init konfiguracemi. Installer očekává vlastní konfigurační soubory. Šifrovaná instalace navíc vyžaduje vyřešení odemknutí při bootu; výchozí návod připouští interaktivní zadání hesla.[^iso]

Projekt `try-omarchy-windows` přidává další užitečné vzory: reprodukovatelný obraz s připnutými balíčky, dohled nad VM, předávání schránky a souborů a volbu grafického vykreslování. Jeho nastavení paměti se podle dokumentace pohybuje mezi 4–8 GiB. Jde o lokální Windows VM produkt, nikoli změřenou hustotu serverových relací.[^try]

| Provedení | Vhodnost | Co musíme dodat |
|---|---|---|
| Omarchy v KVM/libvirt či Proxmox VM | První prototyp plného prostředí | Šablony, identity, vzdálený přístup, řízení relací, obnovu |
| Omarchy upravené do OCI kontejneru | Pozdější experiment s hustotou | Úpravy služeb, runtime adresářů, compositoru, oprávnění a aktualizací |
| Malý desktop v kontejneru | Dobrá varianta pro konkrétní agentní aplikace | Stejné produktové řízení, menší distribučně specifický rozsah |
| Samostatný browser | První volba pro webové procesy | Profily, bezpečné přihlášení, downloady, převzetí a audit |
| Integrace existujícího desktopu | Nejrychlejší ověření hodnoty | Konektor, panel, předávání kontextu a párování identity |

Docker kolem QEMU může pomoci s balením, ale uvnitř stále běží VM. Nepřináší automaticky paměťovou hustotu běžných procesových kontejnerů. Pro KVM je nutný příslušný hostitel. Hetzner například výslovně nepodporuje nested virtualization na Cloud serverech; pro vlastní VM fleet by bylo potřeba použít dedikovaný server nebo jiného vhodného poskytovatele.[^hetzner-faq]

## 4. Co říkají zkušenosti uživatelů

Recenze jsou zkušenosti jednotlivců, nikoli reprezentativní studie. Starší recenze verzí 2/3 nevypovídají přímo o Quickshellu ve verzi 4. Pozorování z konkrétního notebooku také neprokazuje výkon vzdáleného desktopu.

| Zdroj | Pozitivní zkušenost | Problém nebo omezení | Dopad pro produkt |
|---|---|---|---|
| Pranav Karawale, únor 2026 | Připravené prostředí šetří ladění; soudržnost a klávesové ovládání | Chromium, audio, instalační a síťové potíže v jeho sestavě; text nyní uvádí, že systém opustil | Kontrolovat konkrétní pracovní scénáře, ne jen úspěšný boot |
| Jared Smith, září 2026, Apple Silicon | Defaults, svižnost a integrace agentů | Závislost při instalaci, přenositelnost toolchainu a chybějící části dosavadního ekosystému; několik dní používání | Migrace zahrnuje návyky a aplikace; rychlost je subjektivní dojem |
| Alex Hernandez / Techaeris, srpen 2026, Quattro | Snadná instalace, jeho hardware, přizpůsobení agentem | Učení tilingu a klávesového workflow pro běžné uživatele | Kancelářský uživatel musí být účastníkem pilotu |

Tyto zdroje podporují hypotézu, že hlavní síla Omarchy je připravenost a integrace. Nedokládají vyšší produktivitu celé firmy ani nižší TCO.[^review-karawale][^review-smith][^review-techaeris]

Ve veřejných issues jsou současně konkrétní problémy relevantní pro dlouhé relace. #9897 popisuje mimořádný nárůst paměti Quickshellu na v4.0.2-1. #10880 hlásí na v4.0.3-1 a NVIDIA s malou VRAM selhávání grafického kontextu, po kterém menu nereaguje, přestože příkaz vrací úspěch. #6952 popisuje pád při změně audio zařízení.[^issue-oom][^issue-gpu][^issue-audio]

Hlášení jsou podněty k reprodukci, nikoliv potvrzené univerzální diagnózy. Nelze z nich počítat chybovost bez znalosti celé populace a podmínek. Pro Crewship ale ukazují, proč healthcheck musí ověřovat skutečný obraz a vstup, proč potřebujeme paměťové limity a proč je nutný alespoň několikadenní test dlouhé relace.

## 5. Výkon: co víme a co je zatím předpoklad

**Nebyl nalezen důvěryhodný reprodukovatelný benchmark Omarchy 4 ve vzdáleném víceuživatelském nasazení.** Počty uživatelů na server v této analýze proto nejsou garantovaná kapacita. Rovněž nebyl proveden vlastní běh Omarchy ani výkonový benchmark.

Doložené nastavení 8 GiB v ISO testech a 4–8 GiB v projektu pro Windows dává lepší výchozí rámec než tvrzení, že systém někde nabootoval na starém počítači. Přidělená RAM ovšem není naměřená běžná spotřeba ani minimum.[^iso-code][^try]

| Profil pilotu | Počáteční limit RAM | vCPU | Účel a omezení |
|---|---:|---:|---|
| Samostatný browser / lehká agentní úloha | 2–4 GiB | 2 | Malý počet stránek, bez těžkého buildu |
| Lehký desktop | 4 GiB | 2 | Experimentální dolní profil, ověřit reálné aplikace |
| Firemní Omarchy desktop | 8 GiB | 4 | Výchozí pilotní profil; nejde o rezervaci čtyř fyzických jader |
| Vývojářský desktop | 16 GiB | 4–8 | IDE, buildy a kontejnery; kapacitu spočítat odděleně |
| Lokální LLM | Samostatný návrh | Samostatný návrh | Model, kvantizace, VRAM a paralelismus dominují |

Všechny řádky tabulky jsou doporučené testovací konfigurace. Model provozu níže předpokládá externí model přes API, nikoliv lokální inference. Omarchy samotné nenahrazuje GPU či paměť potřebnou pro běh velkého modelu.

Na hostiteli s 64 GiB a rezervou 16 GiB vychází při plné rezervaci 8 GiB **nejvýše šest aktivních relací podle paměti**. CPU, GPU encoder, I/O nebo síť mohou dovolený počet dále snížit. Cíl šest relací proto musí prokázat benchmark; není to příslib, že konkrétní 64GB server šest uživatelů vždy obslouží.

Pro běžné kancelářské prostředí je vhodné zkusit vykreslování bez předané GPU, ale oddělit dvě měření: cenu renderování desktopu a cenu enkódování streamu. GPU passthrough, sdílený render node a virtuální GPU mají různé izolační a kapacitní vlastnosti. Přidat grafickou kartu až po změření úzkého místa.

**Síť a levný klient jsou součást výkonu.** Pilot by měl zkusit RTT 20, 50 a 100 ms, ztrátovost 1 %, Full HD a dva monitory. Navržený cíl je p95 odezvy vstupu pod 150 ms při RTT do 50 ms, nikoliv tvrzení o dnes dosažené latenci. Starý notebook musí zvládat dekódování zvoleného kodeku, aktuální browser a požadované obrazovky.

Při modelových 5 Mbit/s spotřebuje stream asi 2,25 GB za hodinu, tedy 396 GB za 176 hodin. Čtyřicet současně využívaných míst v tomto zjednodušeném modelu představuje 200 Mbit/s a 15,84 TB měsíčně. Skutečný tok je proměnlivý. Video, kamery a dva monitory vyžadují samostatné měření; statický text může být výrazně levnější.

## 6. Návrh hlubší integrace do Crewshipu

### Desktopový konektor a panel

Malý proces v pracovní relaci se odchozím spojením připojí ke Crewshipu. Párování vytvoří identitu zařízení a uživatele; krátkodobé oprávnění stanoví konkrétní dostupné funkce. Panel zobrazuje stav agentky, čekající vstup a dokončené výsledky. Dlouhodobé administrátorské tokeny ani přímý přístup k vaultu do panelu nepatří.

První akce: „Předat soubor Martičce“, „Pokračovat v této úloze“, „Vysvětlit tuto chybu“ a „Otevřít výsledek“. Výběr obsahu je explicitní; nepřetržité snímání obrazovky ani úplná historie clipboardu nejsou výchozí mechanismus paměti.

### Relace jako samostatný spravovaný objekt

Vhodná metadata jsou `workspace_id`, vlastník, profil prostředí, verze image, stav, lease, aktuální ovladač, expirace, poslední aktivita a umístění persistentních dat. Stavový automat může být `provisioning → ready → leased → idle → stopped`, s explicitním stavem `failed` a řízeným přechodem do `deleting`.

Crewship potřebuje rozhraní pro vytvoření, spuštění, zastavení, přidělení relace, přístup k obrazu, omezené vstupy a zničení. VM provider doplní šablony a klonování; souborový snapshot a obnova procesu nesmějí být vydávány za tutéž operaci. Prototyp může využít jeden libvirt/Proxmox host a jednoduchý broker; není nutné současně zavádět Kubernetes.

V současném kódu existují `CLIAdapter`, vlastní image v `CrewConfig`, vykonávání příkazů a lifecycle kontejnerů. Celý kontrakt desktopové relace, převzetí řízení a správy VM z těchto rozhraní hotový nevzniká. Je vhodnější přidat doplňkového providera pracovních relací než deformovat každý kontejnerový `Exec` na ovládání obrazovky.[^crew-runtime]

### Oddělit člověka a autonomní agentku

Společná plocha má vždy jediného aktivního ovladače. Převzetí člověkem musí zneplatnit lease agentky a zahodit dosud nedoručené vstupy. Autonomní práce během lidské práce má mít vlastní browser profil nebo desktop, nikoliv bojovat o focus na stejné ploše.

Soubory se předávají přes jasně vymezený workspace; rozdílné osoby či klienti nedostávají stejný přihlášený profil. Návrh může mít persistentní lidskou VM a krátkodobé agentní prostředí. Není nutné, aby identita agentky byla svázaná s životností konkrétní VM.

### Paměť, dokumenty a relace

| Vrstva | Co obsahuje | Jak přežívá vypnutí desktopu |
|---|---|---|
| Firemní znalosti | Ověřená fakta, návody a dokumenty s původem a ACL | Spravované úložiště a index Crewshipu |
| Paměť agentky | Zkušenosti, preference, stav úloh a odkazy na důkazy | Journal a paměťové služby, řízená konsolidace |
| Pracovní data | Soubory, rozpracované dokumenty, výstupy | Persistentní volume a ověřené zálohy |
| Aplikační relace | Cookies, tokeny, otevřená okna | Podle politiky uchovat, obnovit nebo zrušit |
| RAM procesu | Neuložený runtime stav | Nemusí přežít; garantovat jen výslovně podporovanou obnovu |

Crewship již má kód vyhledávání paměti a episodického recallu. Omarchy mu nedodává prokázaně lepší firemní retrieval. Jeho nejcennější příspěvek je nová cesta k pracovním událostem a uživatelskému kontextu. Starší lokální PRD obsahují historické nálezy; bez nové validace je nelze označit za současné chyby.[^crew-memory]

## 7. Bezpečnostní a provozní hranice

**Plugin je kód, ne dekorace.** Ve v4.0.3 příkaz pro instalaci pluginu výslovně upozorňuje, že plugin běží bez sandboxu v dlouho žijícím shellu. Release současně obsahuje zpevnění přístupu k autentizačním službám a testy této hranice. Tyto dílčí ochrany nejsou plnohodnotný procesový sandbox pro libovolné rozšíření.[^plugin-add][^release][^plugin-auth]

Pro firemní profil bych povolil jen spravované revize rozšíření. Citlivá operace patří do malého brokera s explicitními metodami. Agent ani plugin nesmí získat hostitelský Docker socket, hypervisor API s administrátorskými právy nebo neomezené sudo jen proto, aby šlo prostředí snadno upravovat.

**Přihlášený browser je delegované oprávnění.** Současná sidecar proxy u HTTPS CONNECT do šifrovaného tunelu nevkládá credentials. Nelze na ni přenést předpoklad automatické správy přihlášení do libovolného CRM. Účty, cookies, MFA a odhlašování potřebují vlastní návrh. Předání relace s právem schválit platbu znamená delegovat i toto právo, pokud není mimo relaci další technický gate.[^crew-proxy]

**Obnova vyžaduje ochranu před duplicitou.** Restart agentky po výpadku nesmí zopakovat odeslání faktury, protože obraz disku neobsahuje poslední lokální potvrzení. Externí akce potřebují stabilní identifikátor, kontrolu výsledku a dostupnou evidenci mimo rollbackovaný desktop.

**Rezervní hostitel není automatické HA.** Pro obnovu po ztrátě stroje musí existovat dostupná kopie dat, nová síťová cesta, obnovené identity a ověřený postup. Model níže platí kapacitní rezervu a základní zálohy; negarantuje bezvýpadkový přesun VM ani nulovou ztrátu dat. Finální RPO/RTO se musí navrhnout a nacenit podle zákazníka.

## 8. Co využít pro přenos desktopu

| Kandidát | Silná stránka | Podmínka použití |
|---|---|---|
| Apache Guacamole | HTML5 gateway pro RDP, VNC a SSH, integrační API | Neřeší samo správu OS, relací ani agentní ovládání |
| Selkies | Linuxové streamování, GPU/CPU cesty, klient v browseru | Ověřit konkrétní Wayland/compositor a režim přenosu |
| Kasm Workspaces | Hotová správa streamovaných pracovních prostředí | Ověřit komerční licenci, API a podporovaný desktopový stack |
| QEMU VNC/QMP | Přirozený diagnostický a testovací přístup k VM | QMP ponechat v management síti; není to hotové uživatelské UX |

Guacamole je vhodné jako stavební díl pro první gateway. Selkies je kandidát na kvalitnější streaming, zejména při požadavcích na multimédia; aktuální dokumentace uvádí WebSocket jako výchozí přenos a WebRTC jako volbu. Samotná deklarace podpory Waylandu neprokazuje bezchybnou kombinaci s Omarchy.[^guacamole][^selkies]

Kasm má prakticky relevantní obchodní omezení: Community Edition není obecná bezplatná produkční licence pro firemní službu. Ve zveřejněné tabulce má Starter ceny 10 USD za uživatele nebo 20 USD za relaci, ale Developer API je uvedeno až u jiných edic. Předpoklad „zaplatíme levný Starter a celé to ovládneme přes API“ tedy není podložený. Pro náš rozsah je potřeba ověřená nabídka; do základního modelu licence Kasm zahrnuta není.[^kasm]

Omarchy kód má MIT licenci, ale celý systém obsahuje balíčky s různými licencemi. Komerční distribuce a branding vyžadují zachovat příslušné notices a prověřit konkrétní přibalené aplikace. Cena licence aplikace, Linux desktopu a modelu jsou nezávislé položky.[^license]

## 9. Odhad práce a pořadí realizace

Člověkoden znamená přibližně osm hodin zkušeného inženýra. Rozsahy předpokládají znalost Crewshipu, opětovné využití existujícího vzdáleného přístupu a vývoj s asistencí AI. Nezahrnují vývoj nového compositoru, kodeku, obecného computer-use modelu ani certifikaci odvětvového produktu. AI asistence je započtena kvalitativně; neexistuje podklad pro automatické dělení času deseti.

| Stupeň | Kumulativní práce | Modelová cena | Co je hotové |
|---|---:|---:|---|
| Rozhodovací pilot | 10–15 dní | 100–225 tis. Kč | VM, streaming, základní předání úlohy, měření a uživatelské scénáře |
| Použitelný konektor | 15–25 dní | 150–375 tis. Kč | Párování, panel, soubory/odkazy, identita úlohy, základní distribuce |
| Browserové pracovní relace | 30–50 dní | 300–750 tis. Kč | Profily, přidělování, životní cyklus, takeover a omezený audit |
| Omezený produkt s desktopem agentky | 55–90 dní | 550 tis.–1,35 mil. Kč | VM provider, obnova, limity, aktualizace a vybrané aplikace |
| Firemní desktopová služba | 120–220 dní | 1,2–3,3 mil. Kč | Správa fleet, onboarding/offboarding, provoz, DR, podporované periferie a procesy |

Stupně se **nesčítají**. Pilot je součást cesty; browserová větev a plné Omarchy mají část práce společnou, část alternativní. Port plného Omarchy do OCI by byl další experiment odhadem 15–30 člověkodnů, bez záruky výhodnějšího výsledku. Do základního plánu jej nedoporučuji.

Rozklad firemní varianty vysvětluje velikost odhadu:

| Oblast | Člověkodny |
|---|---:|
| Pracovní scénáře a technický pilot | 10–15 |
| Životní cyklus, VM obrazy a broker | 15–25 |
| Identita, oprávnění a gateway | 15–25 |
| Ovládání agentem a převzetí člověkem | 15–25 |
| Soubory, profily, zálohy a obnova | 15–25 |
| Produktové UI a desktopový konektor | 10–20 |
| Aktualizace, kapacita, měření, rollout | 15–25 |
| Kompatibilita, provozní příprava a zákaznický pilot | 15–35 |
| Integrační rezerva | 10–25 |
| **Celkem** | **120–220** |

Dva lidé mají teoretickou kapacitu kolem 40 člověkodnů za měsíc. Firemní varianta tak aritmeticky odpovídá 3–5,5 měsíce, ale s návaznostmi, zpětnou vazbou a zátěžovými testy je rozumnější plánovat **4–7 kalendářních měsíců**. Nejde o slib termínu.

Průběžná technická údržba nového produktu představuje odhad 4–10 člověkodnů měsíčně podle šíře podpory. Model níže používá 60 tisíc Kč měsíčně pro celý produkt. Zákaznická podpora má samostatný řádek; nesmí se pod tímto odhadem ztratit.

## 10. Náklady na infrastrukturu, AI a službu

### Cenové opory a předpoklady

Oficiální ceník Hetzneru uvádí pro AX42-1 v příslušných evropských lokalitách 97,30 EUR měsíčně bez IPv4 a DPH, setup 49 EUR. Konfigurátor uvádí 64 GB DDR5, procesor Ryzen 7 PRO 8700GE a dva 512GB NVMe disky. Limitované levnější nabídky nejsou garantovaně dostupné a model je nepoužívá.[^hetzner-price][^hetzner-spec]

Přepočty **25 Kč/EUR a 22 Kč/USD jsou zaokrouhlené scénářové vstupy, nikoliv tvrzení o aktuálním kurzu**. Ceny nezahrnují DPH. Sazby práce, podpory, záloh, hustota i marže jsou interní ekonomické předpoklady.

Aktuální primární ceník Claude uvádí pro Sonnet 5 2 USD za milion vstupních a 10 USD za milion výstupních tokenů, cache read 0,20 USD a krátký cache write 2,50 USD. Starší stránka produktu avizovala pozdější sazbu 3/15; model používá aktuální ceník 2/10, ne tuto starší informaci. Ceník neprokazuje vhodnost modelu pro každý typ desktopové úlohy.[^ai-price]

### Průhledný model desktopů pro 10 a 50 uživatelů

Předpoklady: 80% souběžnost; 8 GiB na aktivní desktop; šest relací na hostitele; jeden dodatečný rezervní host; 40 GiB pracovních dat na uživatele; základní zálohy 3 EUR za uživatele/měsíc; gateway a monitoring dohromady 50 EUR/měsíc. Podpora 0,5 hodiny na uživatele/měsíc za interních 750 Kč/h. Provozní rezerva 20 % z infrastruktury a podpory. Pro tvrdé RPO/RTO, externí disky nad tento profil nebo GPU je třeba rozpočet zvýšit.

| Položka měsíčně | Firma 10 uživatelů | Firma 50 uživatelů |
|---|---:|---:|
| Aktivní relace ve špičce | 8 | 40 |
| Hostitelé včetně rezervního | 3 | 8 |
| Výpočetní hostitelé | 291,90 EUR | 778,40 EUR |
| Gateway, monitoring a základní zálohy | 80 EUR | 200 EUR |
| Infrastruktura přepočtená do Kč | 9 298 Kč | 24 460 Kč |
| Základní podpora | 3 750 Kč | 18 750 Kč |
| Provozní rezerva | 2 610 Kč | 8 642 Kč |
| **Celkem bez AI a vývoje** | **15 657 Kč** | **51 852 Kč** |
| **Na uživatele** | **1 566 Kč** | **1 037 Kč** |

Setup hostitelů je jednorázově 147 / 392 EUR a není zahrnutý v měsíčních řádcích. Nasazení firmy, migrace účtů, import dokumentů a školení jsou také samostatné jednorázové náklady. Není zahrnuto placené vzdálené prostředí ani licence kancelářských aplikací.

Rezervní host nezajišťuje kopii všech profilů; její dostupnost je úkolem zálohování a případné replikace. Dva disky v zrcadle nedávají součet své kapacity. Při 40 GiB pracovních dat na uživatele je třeba hlídat i místo pro základní image, copy-on-write změny a snapshoty. Pokud se nebude možné vejít do lokálního storage, základní cena hostitele přestává být úplným nákladem.

U samostatné agentky s nízkou souběžností bude ekonomika jiná: 50 identit nemusí znamenat 40 desktopů. Na dedikovaném serveru ale vypnutí relací samo nesníží fakturu; zvyšuje využitelnou kapacitu. Hetzner Cloud navíc účtuje existující server i při vypnutí. Pro skutečnou hodinovou úsporu je nutná odpovídající politika vytváření/rušení a účtování poskytovatele.[^hetzner-billing]

### AI může převýšit cenu desktopu

Následující scénáře jsou objemy pro rozpočet, nikoliv změřená spotřeba zaměstnance. Započítává se veškerý účtovaný vstup přes jednotlivé kroky, včetně opakovaného kontextu; nejde jen o nově napsaná slova.

| Měsíční vstup / výstup | Cena při 2/10 USD za MTok | Modelový přepočet |
|---|---:|---:|
| 5 mil. / 0,5 mil. | 15 USD | 330 Kč |
| 50 mil. / 5 mil. | 150 USD | 3 300 Kč |
| 200 mil. / 20 mil. | 600 USD | 13 200 Kč |

Tabulka nevyužívá cache ani batch slevy a nezahrnuje zvlášť placené nástroje. Vizuální úlohy musí započítat účtování obrazového vstupu. Účet za subscription nelze automaticky považovat za neomezenou licenci serverového API produktu. Vhodný model je rozpočet na dokončenou úlohu, denní strop a jasné vyúčtování AI spotřeby.[^ai-price]

### Cena zákazníkovi a rozpočítání vývoje

Modelový vývoj 1,8 mil. Kč se při 250 platících uživatelích a 24 měsících rozpouští do 300 Kč za uživatele/měsíc. Průběžná údržba 60 tisíc Kč měsíčně přidá 240 Kč. Při pouhých 50 platících uživatelích jsou stejné položky 1 500 a 1 200 Kč. To je zásadní rozdíl mezi službou pro jednu firmu a opakovatelným produktem.

Při 250 platících uživatelích napříč zákazníky, lehkém AI scénáři a modelové marži 40 % vychází potřebná cena přibližně **4 059 Kč na uživatele u desetimístného zákazníka a 3 178 Kč u padesátimístného zákazníka**. Při středním AI scénáři přibližně 9 009 / 8 128 Kč. Jde o cenu odvozenou z modelu nákladů, ne ověřenou ochotu trhu platit.

Marže se počítá jako `(cena − náklad) / cena`, tedy cenu získáme dělením nákladu 0,6. Není to totéž jako přidat k nákladu 40 %. Obchod, financování, výpadky nad rezervu a náklad na získání zákazníka mohou potřebnou cenu zvýšit. Pokud je AI přefakturovaná samostatně bez marže, vychází jiný ceník.

Na vlastním serveru zákazníka lze část cash nákladů snížit využitím volné kapacity. Stále však existuje amortizace, elektřina, UPS, zálohy, síť, aktualizace a zásahy. Pro ilustraci 60 tisíc Kč za server / 36 měsíců = 1 667 Kč měsíčně, plus modelových 120 W × 730 hodin × 6 Kč/kWh = 526 Kč elektřiny. To není nabídka konkrétního stroje ani úplný TCO.

## 11. Předaná hodnota a obchodní smysl

**Úspora za koncový počítač nestačí jako hlavní argument.** Při hypotetickém rozdílu 12 tisíc Kč mezi dražším a levnějším klientem a životnosti čtyři roky je úspora jen 250 Kč měsíčně. Modelový serverový desktop včetně podpory stojí více. VDI se musí obhájit produktivitou, správou, dostupností pracovního prostředí nebo požadavkem na centralizaci.

Pro zákazníka počítat čistý ušetřený čas: čas původní práce minus zadávání, čekání blokující práci, kontrola výsledku a opravy. Při hodnotě práce 500 Kč/h a 22 pracovních dnech znamená 15 minut denně 2 750 Kč měsíčně a 30 minut 5 500 Kč. Cena 3 178 Kč potřebuje asi 17,3 minuty skutečně využitelné úspory denně; cena 8 128 Kč asi 44,3 minuty. Uvolněný čas se nestává automaticky finanční úsporou, pokud jej firma neumí využít.

Srovnávat je nutné i levnější alternativu: **Crewship s agentkou na stávajícím desktopu může ušetřit podobný čas bez migrace do VDI.** Celou hodnotu agentky nelze připsat Omarchy. V experimentu musí být stejné procesy dostupné také přes browser/API a současné pracovní prostředí.

| Segment | Očekávaná hodnota | Doporučení |
|---|---|---|
| Technické týmy, vývojáři, analytici | Připravené nástroje a společná práce s agentem | Dobrý časný segment pro Omarchy profil |
| Back office s převahou webových systémů | Automatizace exportů a přenosů dat | Nejprve browserové relace, pak ověřit potřebu desktopu |
| Outsourcing/BPO s opakovatelnými úlohami | Sdílené postupy, kontrola, přesné měření času | Dobrý pilot při jasně omezených aplikacích |
| Kancelář s komplexními makry, specializovanými aplikacemi a periferiemi | Hodnota agentky možná, migrace vysoká | Nezačínat nahrazením pracovního OS |
| CAD, střih, videokonference celý den | Drahá grafika, multimédia a periferní integrace | Samostatný produktový a kapacitní návrh |

Pokud nezbytná aplikace běží jen ve Windows, Omarchy není přímé řešení. Vnořená Windows VM uvnitř Omarchy VM přidává komplikace; rozumnější může být samostatný Windows desktop vedle linuxového agenta. Jeho licenční a provozní cena se musí posoudit zvlášť.

## 12. Rozhodovací pilot a měřitelné podmínky pokračování

Pilot má porovnat tři prostředí se stejným modelem a zadáním: stávající workflow + konektor/browser, malý desktop a Omarchy VM. Zapojit alespoň tři technické a tři netechnické uživatele. Délka uživatelského ověření nejméně dva pracovní týdny; 10–15 člověkodnů práce lze rozložit do delšího kalendářního období.

Pracovní scénáře: zpracování exportu z testovacího CRM, příprava a kontrola dokumentu, práce s interní aplikací, předání rozpracované úlohy, přihlášení s lidským krokem, česká klávesnice a přenos souboru. Dále restart browseru, pád shellu, OOM, ztráta sítě, hostitelský restart a obnova ze zálohy. Pro zákaznické akce používat testovací účty a data.

| Gate | Navržená podmínka pro pokračování |
|---|---|
| Technická cesta | Běh bez privilegovaného přístupu agenta k hostiteli a bez veřejného management rozhraní |
| Kapacita | Ověřit 1, 3 a 6 aktivních 8GiB relací; limit snížit při degradaci |
| Stabilita | Nejméně 72 hodin zátěžového běhu, trend paměti a reakce na řízené pády |
| Ovládání | Stop a takeover zastaví i frontu vstupů; žádné kliknutí po předání řízení |
| Obnova | Předem určená RPO/RTO, skutečný restore, žádná duplicita externí akce |
| Izolace | Jiná identita nezíská obraz, cookies, clipboard ani soubory cizí relace |
| Užitečnost | Alespoň 90 % úspěšných běhů vybraných scénářů a známý čas kontroly; ne důkaz pro libovolnou práci |
| Ekonomika | Změřit dokončené úlohy, tokeny, podporu a čistý čas; hodnotu porovnat s plnou navrženou cenou |
| Adopce | Netechnický uživatel zvládne každodenní úkoly bez asistence vývojáře |

Na každý vybraný automatizovaný scénář je vhodné alespoň 20 opakování; uvést počet chyb i rozptyl času, ne pouze nejlepší demo. S tak malým vzorkem nelze garantovat produkční spolehlivost; lze odhalit zásadně nevhodnou cestu.

Ukončit či změnit směr, pokud plný desktop proti browserové variantě nepřináší užitečnou novou schopnost, podpora převyšuje hodnotu, potřebná aplikace není kompatibilní nebo bezpečnost vyžaduje neomezené oprávnění. Pokračovat do firemní služby až při konkrétních platících zájemcích a ověřené ochotě přijmout celkovou cenu.

## 13. Co převzít a co neopakovat

**Převzít:** popsané příkazy dostupné z více rozhraní; připravené role a aplikace; explicitní předání kontextu; diagnostiku založenou na důkazech; oddělení dat a zobrazení metrik; praktické akceptační testy živé VM; konzistentní ovládání; omezené a pozorovatelné restarty.

**Neopakovat v našem prostředí:** libovolný plugin ve stejném procesu jako důvěryhodné funkce; individuální systémové úpravy agentkou bez fleet politiky; automatické schvalování zaměněné za autorizaci; lokální transcript zaměněný za autoritativní audit; přihlášený browser zaměněný za bezpečný credential broker; „proces žije“ zaměněné za funkční UI; uložený disk zaměněný za paměť agentky.

Nejsilnější odlišitelnost Crewshipu může být **pracovní kontinuita**: agentka zná úlohu a oprávnění, umí převzít omezený kontext z prostředí člověka, pracovat ve vlastním prostředí a vrátit ověřitelný výsledek. Omarchy nabízí dobré místo, kde tuto zkušenost předvést. Důvod k nákupu však musí být dokončená práce a její ekonomika.

## 14. Rozsah ověření a otevřené otázky

Podklady byly ověřovány k 11. září 2026. Zdrojový rozbor pokryl stabilní tag Omarchy a vybrané změny vývojové větve, související ISO/Windows projekty, hlavní integrační cesty Crewshipu a uvedené veřejné recenze a issues. Nebyl proveden úplný bezpečnostní audit všech souborů, spuštění externích testů ani instalace Omarchy. Čtení kódu dokládá implementovaný mechanismus, nikoliv jeho bezchybný běh.

Crewship byl posuzován ve stromu `005470ed` v instanci 3. Existující rozpracované změny nebyly upravovány. Historické designové dokumenty slouží pro kontext; jejich staré naměřené chyby nejsou vydávány za aktuální stav.

Před investicí do plného produktu zůstává zjistit: přesné zákaznické aplikace a periferie; míra souběžnosti; kdo je vlastníkem přihlášených účtů; požadované RPO/RTO a podpora; cloud versus zákaznický server; čistá časová úspora; reálná hustota relací; skutečná AI spotřeba; dostupnost a podmínky komerčních komponent. Interaktivní ekonomický model a tabulka umožňují tyto předpoklady měnit.

## Zdroje

[^release]: Omacom. [Omarchy v4.0.3](https://github.com/omacom/omarchy/releases/tag/v4.0.3), 8. 9. 2026. Stabilní tag `0534987009061cbe2dacdde4ad564092ab698d12`; releasové funkce a bezpečnostní opravy.
[^commands]: Omacom. [Dispatcher `bin/omarchy`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy) a [metadata příkazů](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/agents/skills/command-metadata.md). Zdrojový kód v4.0.3; počty jsou inventura souborů tagu.
[^agent]: Omacom. [`bin/omarchy-agent`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-agent) a [`omarchy-agent-prompt`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-agent-prompt). Zdrojový kód v4.0.3.
[^crash]: Omacom. [`omarchy-agent-crash`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-agent-crash) a [diagnose-crash skill](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/default/agents/skills/diagnose-crash/SKILL.md). Zdrojový kód a instrukce v4.0.3.
[^plugins]: Omacom. [First-party plugins](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/shell/plugins/README.md). Architektura pluginů a menu, v4.0.3.
[^usage]: Omacom. [Agents panel](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/shell/plugins/agents/README.md). Datový kontrakt a zdroje spotřeby, v4.0.3.
[^webapps]: Omacom. [`omarchy-webapp-install`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-webapp-install) a [`omarchy-theme-set`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-theme-set), v4.0.3.
[^updates-code]: Omacom. [`omarchy-update`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-update), [`omarchy-migrate`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-migrate) a [Updates manual](https://omarchy.org/manual/updates/). Kód v4.0.3, manuál přístup 11. 9. 2026.
[^shell]: Omacom. [`omarchy-launch-shell`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-launch-shell) a [`omarchy-restart-shell`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-restart-shell), v4.0.3.
[^tests]: Omacom. [`test/all`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/test/all) a [`test/acceptance`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/test/acceptance), v4.0.3. Suite byly čteny, nikoliv spouštěny.
[^iso]: Omacom. [Omarchy ISO README](https://github.com/omacom/omarchy-iso/blob/a23f8d464dcb0616a61bfaa8026e23d0533da209/README.md). Revize `a23f8d4`, přístup 11. 9. 2026. Autoinstall, Proxmox a omezení šifrovaného bootu.
[^iso-code]: Omacom. [`omarchy-iso-test`](https://github.com/omacom/omarchy-iso/blob/a23f8d464dcb0616a61bfaa8026e23d0533da209/bin/omarchy-iso-test). Konfigurace VM, QMP a živý test instalace; přístup 11. 9. 2026.
[^try]: Omacom. [Try Omarchy for Windows](https://github.com/omacom/try-omarchy-windows/blob/7fb4fb18519302b3d68ec662234eabceffbc245c/README.md). Revize `7fb4fb1`, přístup 11. 9. 2026; velikost RAM je konfigurace launcheru, ne benchmark.
[^review-karawale]: Pranav Karawale. [Back and forth with Omarchy](https://karawale.com/posts/back-and-forth-with-omarchy), 22. 2. 2026. První osoba, starší generace, individuální zkušenost.
[^review-smith]: Jared Smith. [I Put Omarchy on an M1 MacBook Pro](https://sublimecoding.com/blog/omarchy-asahi-m1-macbook), 7. 9. 2026. Krátký první osobní provozní report, nikoliv benchmark.
[^review-techaeris]: Alex Hernandez. [Omarchy Quattro Review](https://techaeris.com/2026/08/25/omarchy-quattro-review-year-linux-desktop/), Techaeris, 25. 8. 2026. První dojmy a použitelnost, nikoliv studie produktivity.
[^issue-oom]: austrasien. [Issue #9897](https://github.com/omacom/omarchy/issues/9897), 2. 9. 2026. Hlášený runaway memory leak na v4.0.2-1; rozsah dopadu neověřen.
[^issue-gpu]: wbohannon. [Issue #10880](https://github.com/omacom/omarchy/issues/10880), 8. 9. 2026. Hlášené chyby EGL/VRAM na konkrétní NVIDIA konfiguraci.
[^issue-audio]: sanjyay. [Issue #6952](https://github.com/omacom/omarchy/issues/6952), 15. 8. 2026. Hlášený pád při změnách PipeWire zařízení.
[^crew-runtime]: Crewship, commit `005470ed`. Lokální [`CLIAdapter`](../../internal/orchestrator/cli_adapter.go), [`CrewConfig` a provider](../../internal/provider/container.go), [Docker runtime](../../internal/provider/docker/docker_container.go), [lifecycle](../../internal/orchestrator/orchestrator_lifecycle.go). Přímé čtení pracovního stromu instance 3, 11. 9. 2026.
[^crew-memory]: Crewship, commit `005470ed`. Lokální [memory search](../../internal/memory/search.go), [hybrid recall](../../internal/episodic/hybrid.go), [návrh identity](agent-identity-signing.md), [historický retrieval rozbor](memory-retrieval-layer.md). Kód a design mají odlišný status.
[^crew-proxy]: Crewship, commit `005470ed`. Lokální [`handleConnect`](../../internal/sidecar/proxy.go), komentář a implementace kolem řádku 508, přístup 11. 9. 2026.
[^plugin-add]: Omacom. [`omarchy-plugin-add`](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy-plugin-add), v4.0.3. Explicitní upozornění na nesandboxované pluginy.
[^plugin-auth]: Omacom. [Plugin authentication boundary test](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/test/shell.d/plugin-auth-boundary-test.sh), v4.0.3. Dílčí oddělení autentizačních služeb.
[^guacamole]: Apache Software Foundation. [Apache Guacamole](https://guacamole.apache.org/), přístup 11. 9. 2026. Protokoly, HTML5 klient, API a licence.
[^selkies]: Selkies project. [Dokumentace](https://docs.selkies.io/), přístup 11. 9. 2026. Přenosové režimy, Linux/Wayland, klient a licenční informace.
[^kasm]: Kasm Technologies. [Community Edition a srovnání edic](https://kasm.com/community-edition), [Licensing](https://www.kasmweb.com/docs/develop/license.html), přístup 11. 9. 2026. Ceny v tabulce, komerční použití a dostupnost Developer API; finální nabídka neověřena.
[^license]: Omacom. [Omarchy LICENSE](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/LICENSE), v4.0.3. Licence zdrojového projektu.
[^hetzner-price]: Hetzner. [Price Adjustment 15 June 2026](https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/), aktualizace 8. 7. 2026, přístup 11. 9. 2026. AX42-1 měsíční cena a setup, bez IPv4 a DPH.
[^hetzner-spec]: Hetzner. [AX42 configurator](https://www.hetzner.com/dedicated-rootserver/ax42/configurator/), přístup 11. 9. 2026. Specifikace hostitele; dostupnost se může měnit.
[^hetzner-faq]: Hetzner. [Cloud Servers FAQ](https://docs.hetzner.com/cloud/servers/faq/), přístup 11. 9. 2026. Nested virtualization není podporována.
[^hetzner-billing]: Hetzner. [Support FAQ](https://www.hetzner.com/support/), přístup 11. 9. 2026. Účtování existujících vypnutých serverů.
[^ai-price]: Anthropic. [Claude pricing](https://claude.com/pricing), přístup 11. 9. 2026. Aktuální Sonnet 5 input/output a caching ceny. Starší [produktová stránka Sonnet](https://www.anthropic.com/claude/sonnet) obsahuje jinou informaci o konci úvodního ceníku; ve výpočtu má přednost aktuální hlavní ceník.

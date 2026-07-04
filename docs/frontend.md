# Übersicht

Das Frontend von `media-lens` ist eine Next.js 15-Anwendung unter `src/frontend/`. Es nutzt den App Router von Next.js, TypeScript, Tailwind CSS mit custom CSS-Variablen und `lucide-react` für Icons.

Das Frontend zeigt Podcasts, Episoden-Details, Suche und einen interaktiven Player mit Transcript, Emotion-Chart, Fact-Check-Ansicht und KI-Chat.

**Stack:**
- Next.js 15 (App Router)
- TypeScript
- Tailwind CSS (mit custom CSS-Variablen für Theming)
- Go-Backend auf Port 8080

---

## Ordnerstruktur

```text
src/frontend/
├── app/                     # Next.js App-Router Pages
│   ├── globals.css          # globale Styles + CSS-Variablen
│   ├── layout.tsx           # RootLayout mit Sidebar + Page-Container
│   ├── page.tsx             # Startseite / Home
│   ├── explore/page.tsx     # Gesamtansicht aller Episoden
│   ├── suche/page.tsx       # Suche
│   └── podcasts/
│       └── [id]/page.tsx    # Episoden-Detailseite
├── components/
│   ├── layout/
│   │   ├── sidebar.tsx
│   │   └── searchbar.tsx
│   └── ui/
│       ├── audio.tsx        # AudioPlayer
│       ├── card.tsx         # InfoCard
│       ├── cardThemen.tsx   # TopicCard
│       ├── chart.tsx        # EmotionChart
│       ├── chat.tsx         # KI-Chat
│       ├── factcheck.tsx    # FactCheckCard
│       ├── podcastClient.tsx # Haupt-Client-Komponente Detailseite
│       ├── podcastplayer.tsx # Player-Wrapper
│       └── transcript.tsx   # Transcript
├── lib/
│   ├── api.ts               # Backend-API-Aufrufe
│   └── types.ts             # Typdefinitionen
├── public/                  # statische Assets
├── api-contracts.yml        # API-Schnittstelle zum Backend
├── Dockerfile*
├── package.json
└── README.md
```

> **Hinweis zu Next.js 15:** `searchParams` und `params` in Page-Komponenten müssen als `Promise<{...}>` typisiert und mit `await` aufgelöst werden.

---

## Wichtige Seiten

### `app/page.tsx` – Startseite

- Zeigt eine paginierte Liste von Episoden als `InfoCard`
- Lädt Episoden über `fetchEpisodes` aus `lib/api.ts`
- Serverseitig gerendert

### `app/podcasts/[id]/page.tsx` – Episoden-Detailseite

Serverseitige Komponente. Lädt parallel via `Promise.all`:
- Episode-Metadaten (`fetchEpisode`)
- Kapitel (`fetchChapters`)
- Transkript (`fetchTranscript`)
- Fact-Checks (`fetchFactChecks`)
- Empfohlene Episoden (`fetchEpisodes`)

Aggregiert Transkript-Lines und Fact-Checks pro Kapitel und übergibt alles an `PodcastDetailClient`.

**Wichtige Hilfsfunktionen auf dieser Seite:**
- `safeVerdict()` – normalisiert unbekannte Verdict-Strings auf `UNVERIFIABLE`
- `formatDuration()` – formatiert Sekunden als `H:MM:SS` oder `M:SS`

### `app/explore/page.tsx` – Gesamtansicht

- Zeigt alle verfügbaren Episoden in einer Library-Ansicht
- Struktur ähnlich der Startseite, mit vollständiger Episodenliste

### `app/suche/page.tsx` – Suche

- Verwendet `SearchBar` mit URL-Parametern `q` und `type`
- Zeigt bestes Ergebnis, Episodenliste und weitere Insights
- Fallback auf `fetchEpisodes` wenn Search-Service nicht verfügbar

### `app/layout.tsx` – RootLayout

- Fügt `Sidebar` auf allen Seiten ein
- Nutzt Google Fonts (`Courier_Prime`, `DM_Sans`)
- Bindet `globals.css` ein

---

## Kern-Logik und Datenfluss

### `lib/api.ts`

Kapselt alle Backend-Aufrufe.

| Funktion | Endpoint |
|---|---|
| `fetchEpisodes` | `GET /api/v1/episodes` |
| `fetchEpisode` | `GET /api/v1/episodes/${id}` |
| `fetchChapters` | `GET /api/v1/episodes/${id}/chapters` |
| `fetchTranscript` | `GET /api/v1/episodes/${id}/transcript` |
| `fetchFactChecks` | `GET /api/v1/episodes/${id}/fact-checks` |
| `fetchSearch` | `GET /api/v1/search` |

- `BACKEND_URL` aus `process.env.BACKEND_URL`, Standard `http://localhost:8080`
- `getPublicBackendUrl()` verwendet `NEXT_PUBLIC_BACKEND_URL` für Audio-Streaming im Browser
- `backendFetch()` wirft bei HTTP-Fehlern eine Ausnahme
- Bei `fetchSearch`-Fehler: automatischer Fallback auf `fetchEpisodes`

### `lib/types.ts`

Zentrale Typen:

| Typ | Beschreibung |
|---|---|
| `EpisodeCard` | Kurzform einer Episode für Listen |
| `EpisodeDetail` | Vollständige Episode mit allen Feldern |
| `Chapter` | Kapitel mit `transcript_lines` und `fact_checked_claims` |
| `TranscriptLine` | Einzelne Transkriptzeile mit Zeitstempel und Emotion |
| `FactCheckedClaim` | Claim mit Verdict, Erklärung und Quellen |
| `FactVerdict` | Union: `TRUE \| MOSTLY_TRUE \| MISLEADING \| FALSE \| UNVERIFIABLE` |
| `SearchResultItem` | Suchergebnis mit Highlights |

---

## UI-Komponenten

### `audio.tsx` – AudioPlayer

**Props:**
```ts
interface AudioPlayerProps {
  src: string;
  onTimeUpdate?: (time: number) => void;
  seekRef?: MutableRefObject<((time: number) => void) | null>;
}
```

**Funktionsweise:**
- Verwaltet `isPlaying`, `currentTime` und `duration` als lokalen State
- `seekRef` ermöglicht externem Code (z.B. Transcript-Klick) das Springen zu einer Zeitposition
- Beim Seek von außen startet das Audio automatisch

**Duration-Handling:** Drei Event-Listener für zuverlässige Duration-Erkennung:
- `loadedmetadata` – Normalfall
- `durationchange` – Fallback bei Streaming
- `readyState >= 1` Check beim Mount – Fallback bei Cache

`preload="metadata"` sorgt dafür dass die Duration sofort verfügbar ist.

---

### `chart.tsx` – EmotionChart

Die komplexeste UI-Komponente. Zeichnet einen scrollbaren Canvas-Chart.

**Props:**
```ts
interface EmotionChartProps {
  data: EmotionData;       // Segments mit start, end, dominant, score
  currentTime?: number;    // aktuelle Abspielposition in Sekunden
  onSeek?: (time: number) => void; // Callback bei Klick
}
```

**Konstanten:**
```ts
const PX_PER_SECOND = 4;       // Canvas-Breite pro Sekunde
const MIN_CANVAS_WIDTH = 600;   // Mindestbreite
const CANVAS_H = 180;           // Canvas-Höhe
```

**Aufbau:**
Zwei übereinanderliegende Canvas-Elemente:
1. **Haupt-Canvas** (`canvasRef`) – zeichnet Grid, Linie, Punkte und Zeitlabels. Wird nur bei Datenänderung neu gezeichnet.
2. **Playhead-Canvas** (`playheadRef`) – zeichnet die gestrichelte Playhead-Linie. Wird bei jedem `currentTime`-Update neu gezeichnet.

**Scroll-Verhalten:**
- `scrollRef` enthält den scrollbaren Container
- Auto-Scroll hält den Playhead mit einem Rand von 80px im Sichtbereich
- Farbbalken und Chart scrollen gemeinsam in einem Container

**Klick-Seek:**
- Klick auf einen Punkt: seek zum `segment.start` des nächsten Punkts (Toleranz: 30px)
- Klick zwischen Punkten: seek zur exakten geklickten Zeit

**Emotionsfarben:**
```ts
const EMOTION_COLORS = {
  angry:   "#E24B4A",
  happy:   "#dcd354",
  neutral: "#888780",
  sad:     "#9076cc",
};
```

**Legende:** Zeigt immer alle vier Emotionen (`ALL_EMOTIONS`), unabhängig davon welche im Datensatz vorhanden sind.

**Zeitlegende:** Startzeit (links) und Endzeit (rechts) auf der X-Achse.

---

### `podcastClient.tsx` – PodcastDetailClient

Zentrale Client-Komponente der Episodenseite. Hält den gesamten interaktiven State.

**Props:**
```ts
interface PodcastDetailClientProps {
  src: string;
  episodeId: string;
  chapters: Chapter[];
  emotionData: {
    segments: { start: number; end: number; dominant: string; score: number }[];
  };
}
```

**State:**
| State | Typ | Beschreibung |
|---|---|---|
| `panels` | `PanelConfig` | Welche Panels sichtbar sind |
| `currentTime` | `number` | Aktuelle Abspielposition |
| `filteredChapterIndex` | `number \| null` | Aktives Kapitel für Faktencheck-Filter |
| `seekRef` | `MutableRefObject` | Referenz zur Seek-Funktion des AudioPlayers |

**Panel-Konfiguration:**
```ts
interface PanelConfig {
  transcript: boolean;
  emotionChart: boolean;
  themen: boolean;
  faktencheck: boolean;
  chat: boolean;
}
```

Jedes Panel kann über die Konfigurations-Leiste rechts ein- und ausgeblendet werden.

**Layout-Struktur:**
```
┌─────────────────────────────┬──────────────┐
│ AudioPlayer                 │              │
│ EmotionChart                │ Konfiguration│
└─────────────────────────────┴──────────────┘
┌──────────────────────────────────────────────┐
│ Transcript (100%)                            │
├─────────────────────┬────────────────────────┤
│ Chapters (50%)      │ Factcheck (50%)         │
└─────────────────────┴────────────────────────┘
│ KI-Chat (100%)                               │
```

**Wichtig:** Der Chart-Container benötigt `flex-1 min-w-0` damit der scrollbare Canvas nicht aus dem Flex-Layout ausbricht.

---

### `podcastplayer.tsx` – PodcastPlayer

Schlanker Wrapper-Container für AudioPlayer und den EmotionChart-Slot.

**Props:**
```ts
interface PodcastPlayerProps {
  src: string;
  chapters: Chapter[];
  children?: ReactNode;           // Slot für EmotionChart
  onTimeUpdate?: (time: number) => void;
  seekRef?: MutableRefObject<((time: number) => void) | null>;
}
```

Gibt `seekRef` und `onTimeUpdate` direkt an `AudioPlayer` weiter. Enthält keinen eigenen State.

---

### `transcript.tsx` – Transcript

**Props:**
```ts
interface TranscriptProps {
  lines: TranscriptLine[];
  currentTime: number;
  onLineClick: (start: number) => void;
}
```

- Bestimmt aktive Zeile via `currentTime >= line.start_time && currentTime < line.end_time`
- Scrollt automatisch zur aktiven Zeile via `scrollIntoView`
- Klick auf Zeile ruft `onLineClick(line.start_time)` auf → seek im AudioPlayer
- Maximale Höhe `max-h-96` mit `overflow-y-auto`

---

### `factcheck.tsx` – FactCheckCard

**Props:**
```ts
interface FactCheckCardProps {
  claim: FactCheckedClaim;
}
```

Styling pro Verdict:

| Verdict | Label | Farbe |
|---|---|---|
| `TRUE` | Wahr | Grün (`success`) |
| `MOSTLY_TRUE` | Überwiegend wahr | Grün (`success`) |
| `MISLEADING` | Irreführend | Gelb (`warning`) |
| `FALSE` | Falsch | Rot (`danger`) |
| `UNVERIFIABLE` | Unprüfbar | Neutral |

---

### `cardThemen.tsx` – TopicCard

**Props:**
```ts
interface TopicCardProps {
  topic: { name: string; summary: string; start: number; end: number };
  isActive: boolean;      // aktuell abgespieltes Kapitel
  isFiltered: boolean;    // Faktencheck-Filter aktiv für dieses Kapitel
  onSelect: (start: number) => void;
  onFilterToggle: () => void;
}
```

- Klick auf Karte: seek zum Kapitelstart
- „Fakten zeigen"-Button: filtert Faktencheck auf dieses Kapitel
- `isActive` wird durch Vergleich von `currentTime` mit `start_time`/`end_time` in `podcastClient.tsx` bestimmt

---

### `card.tsx` – InfoCard

Darstellung einzelner Episoden in Kachelform für Listen- und Explore-Ansichten.
- Verlinkt auf `/podcasts/${id}`
- Zeigt Cover, Titel, Podcastname, Datum und Dauer

---

### `chat.tsx` – KI-Chat

- Sendet Fragen an `POST /api/v1/episodes/${episodeId}/chat`
- Streaming-Response via NDJSON
- Markdown-Rendering der Antworten
- Internes Timestamp-Parsing: erkannte Zeitmarken in Antworten ermöglichen Sprung im Audio

---

### `sidebar.tsx` – Sidebar

Navigation:
- `/` – Home
- `/explore` – Explore (alle Episoden)

---

### `searchbar.tsx` – SearchBar

- Debounced Suche (300ms)
- Aktualisiert URL-Parameter `q` und `type` via `useRouter()`
- Filter-Tabs: `episode`, `chapter`, `podcast`
- Nutzt `useTransition()` für nicht-blockierende Navigation

---

## Rendering-Modell

| Komponente | Rendering |
|---|---|
| `app/page.tsx` | Server |
| `app/podcasts/[id]/page.tsx` | Server |
| `app/explore/page.tsx` | Server |
| `app/suche/page.tsx` | Server |
| `podcastClient.tsx` | Client (`"use client"`) |
| `AudioPlayer` | Client |
| `EmotionChart` | Client (dynamisch geladen) |
| `Transcript` | Client |
| `Chat` | Client (dynamisch geladen) |
| `SearchBar` | Client |

`EmotionChart` und `Chat` werden via `next/dynamic` lazy-geladen um das initiale Bundle klein zu halten.

---

## Build & Laufzeit

### Entwicklung

```bash
cd src/frontend
npm install
npm run dev
```

### Produktion

```bash
npm run build
npm run start
```

### Umgebungsvariablen

| Variable | Verwendung | Default |
|---|---|---|
| `BACKEND_URL` | Server-seitiger API-Fetch | `http://localhost:8080` |
| `NEXT_PUBLIC_BACKEND_URL` | Browser-seitiges Audio-Streaming | `http://localhost:8080` |

---

## Bekannte Implementierungsdetails

**Canvas-Koordinatensystem:** Alle Canvas-Zeichenoperationen berücksichtigen `devicePixelRatio` für scharfe Darstellung auf Retina-Displays. Canvas-Breite und -Höhe werden in physischen Pixeln gesetzt, gezeichnet wird im logischen Koordinatensystem nach `ctx.scale(dpr, dpr)`.

**CSS-Variablen im Canvas:** Canvas-Kontext kann keine CSS-Variablen direkt lesen. Farben werden via `getComputedStyle(document.documentElement).getPropertyValue(...)` ausgelesen.

**Next.js 15 `params`/`searchParams`:** Müssen als `Promise<{...}>` typisiert und mit `await` aufgelöst werden.
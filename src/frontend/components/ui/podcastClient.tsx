"use client";

import type { Chapter } from "@/lib/types";
import dynamic from "next/dynamic";
import { useState, useRef, MutableRefObject } from "react";
import PodcastPlayer from "@/components/ui/podcastplayer";
import { TopicCard } from "@/components/ui/cardThemen";
import { FactCheckCard } from "@/components/ui/factcheck";
import Transcript from "@/components/ui/transcript";

const EmotionChart = dynamic(() => import("@/components/ui/chart"));
const Chat = dynamic(() => import("@/components/ui/chat"));

interface PanelConfig {
  transcript: boolean;
  emotionChart: boolean;
  themen: boolean;
  faktencheck: boolean;
  chat: boolean;
}

const PANEL_LABELS: Record<keyof PanelConfig, string> = {
  transcript:   "Transcript",
  emotionChart: "Emotionschart",
  themen:       "Chapters",
  faktencheck:  "Factcheck",
  chat:         "AI-Chat",
};

interface PodcastDetailClientProps {
  src: string;
  episodeId: string;
  chapters: Chapter[];
  emotionData: {
    segments: { start: number; end: number; dominant: string; score: number }[];
  };
}

export default function PodcastDetailClient({
  src,
  episodeId,
  chapters,
  emotionData,
}: PodcastDetailClientProps) {
  const [panels, setPanels] = useState<PanelConfig>({
    transcript:   true,
    emotionChart: true,
    themen:       true,
    faktencheck:  true,
    chat:         true,
  });

  const [currentTime, setCurrentTime] = useState<number>(0);
  const [filteredChapterIndex, setFilteredChapterIndex] = useState<number | null>(null);
  const seekRef = useRef<((time: number) => void) | null>(null);

  const toggle = (key: keyof PanelConfig) => {
    setPanels((prev) => ({ ...prev, [key]: !prev[key] }));
  };

  const sorted = [...chapters].sort((a, b) => a.chapter_idx - b.chapter_idx);

  const activeChapterIndex = sorted.findIndex(
    (ch) => currentTime >= ch.start_time && currentTime < ch.end_time
  );

  const handleSeek = (time: number) => seekRef.current?.(time);

  const handleFilterToggle = (index: number) => {
    setFilteredChapterIndex((prev) => (prev === index ? null : index));
  };

  const allLines = sorted.flatMap((ch) => ch.transcript_lines ?? []);

  const visibleClaims =
    filteredChapterIndex !== null
      ? sorted[filteredChapterIndex]?.fact_checked_claims ?? []
      : sorted.flatMap((ch) => ch.fact_checked_claims ?? []);

  const filteredChapterName =
    filteredChapterIndex !== null ? sorted[filteredChapterIndex]?.title : null;

  return (
    <div className="flex flex-col gap-6 w-full bg-background-raised mt-6 px-4 py-6">

      {/* Oben: Player + EmotionChart links, Menü rechts */}
      <div className="flex flex-row gap-6 min-w-0">

        {/* flex-1 min-w-0 verhindert dass der Chart aus dem Flex-Container rausbricht */}
        <div className="flex-1 min-w-0">
          <PodcastPlayer
            src={src}
            chapters={sorted}
            onTimeUpdate={setCurrentTime}
            seekRef={seekRef as MutableRefObject<((time: number) => void) | null>}
          >
            {panels.emotionChart && (
            <EmotionChart
              data={emotionData}
              currentTime={currentTime}
              onSeek={handleSeek}
            />
            )}
          </PodcastPlayer>
        </div>

        {/* Konfigurations-Panel */}
        <div className="w-64 shrink-0">
          <p className="text-lg font-medium text-foreground-subtle mb-4">Configuration</p>
          <div className="flex flex-col gap-2">
            {(Object.keys(panels) as (keyof PanelConfig)[]).map((key) => (
              <button
                key={key}
                onClick={() => toggle(key)}
                className={`flex items-center justify-between w-full px-3 py-2.5 rounded-lg border text-sm transition-all cursor-pointer
                  ${panels[key]
                    ? "bg-primary-muted border-primary text-foreground"
                    : "bg-background-card border-border text-foreground-subtle hover:bg-background-raised"
                  }`}
              >
                <span>{PANEL_LABELS[key]}</span>
                <span className={`w-4 h-4 rounded-full border-2 transition-colors ${panels[key] ? "bg-primary border-primary" : "border-foreground-subtle"}`} />
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Transkript – volle Breite */}
      {panels.transcript && allLines.length > 0 && (
        <div>
          <p className="text-lg text-foreground-subtle mb-3">Transcript</p>
          <Transcript
            lines={allLines}
            currentTime={currentTime}
            onLineClick={handleSeek}
          />
        </div>
      )}

      {/* Chapters + Faktencheck – volle Breite, 50/50 */}
      {(panels.themen || panels.faktencheck) && (
        <div className="flex flex-row gap-6 items-start">

          {panels.themen && (
            <div className="w-1/2">
              <p className="text-lg text-foreground-subtle mb-3">Chapters</p>
              <div className="flex flex-col gap-3">
                {sorted.map((chapter, index) => (
                  <TopicCard
                    key={chapter.id}
                    topic={{
                      name: chapter.title ?? `Kapitel ${chapter.chapter_idx + 1}`,
                      summary: chapter.summary ?? "",
                      start: chapter.start_time,
                      end: chapter.end_time,
                    }}
                    isActive={activeChapterIndex === index}
                    isFiltered={filteredChapterIndex === index}
                    onSelect={handleSeek}
                    onFilterToggle={() => handleFilterToggle(index)}
                  />
                ))}
              </div>
            </div>
          )}

          {panels.faktencheck && (
            <div className="w-1/2">
              <div className="flex items-center justify-between mb-3">
                <p className="text-lg text-foreground-subtle">
                  Factcheck
                  {filteredChapterName && (
                    <span className="ml-2 text-xs text-accent">· {filteredChapterName}</span>
                  )}
                </p>
                {filteredChapterIndex !== null && (
                  <button
                    onClick={() => setFilteredChapterIndex(null)}
                    className="text-xs text-foreground-subtle hover:text-foreground transition-colors cursor-pointer"
                  >
                    Alle anzeigen ×
                  </button>
                )}
              </div>
              <div className="flex flex-col gap-3">
                {visibleClaims.length > 0 ? (
                  visibleClaims.map((claim) => (
                    <FactCheckCard key={claim.id} claim={claim} />
                  ))
                ) : (
                  <p className="text-xs text-foreground-subtle">
                    Keine Fakten für dieses Thema vorhanden.
                  </p>
                )}
              </div>
            </div>
          )}

        </div>
      )}

      {/* KI-Chat – volle Breite */}
      {panels.chat && <Chat episodeId={episodeId} onSeek={handleSeek} />}

    </div>
  );
}
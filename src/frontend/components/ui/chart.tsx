"use client";

import { useCallback, useEffect, useRef, useState } from "react";

export interface EmotionSegment {
  start: number;
  end: number;
  dominant: string;
  score: number; // 0–1
}

export interface EmotionData {
  segments: EmotionSegment[];
}

interface EmotionChartProps {
  data: EmotionData;
  currentTime?: number;
  onSeek?: (time: number) => void;
}

const EMOTION_COLORS: Record<string, string> = {
  angry:   "#E24B4A",
  happy:   "#dcd354",
  neutral: "#888780",
  sad:     "#9076cc",
};

const ALL_EMOTIONS = ["angry", "happy", "neutral", "sad"];

const PX_PER_SECOND = 4;
const MIN_CANVAS_WIDTH = 600;
const MAX_CANVAS_WIDTH = 8000; // Safe logical limit to prevent exceeding max canvas area
const CANVAS_H = 180;
const PAD_L = 12;
const PAD_R = 12;
const PAD_T = 16;
const PAD_B = 28;

function formatTime(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = Math.floor(seconds % 60);
  return `${m.toString().padStart(2, "0")}:${s.toString().padStart(2, "0")}`;
}

function getColor(emotion: string): string {
  return EMOTION_COLORS[emotion] ?? "#888780";
}

export default function EmotionChart({ data, currentTime = 0, onSeek }: EmotionChartProps) {
  const canvasRef   = useRef<HTMLCanvasElement>(null);
  const playheadRef = useRef<HTMLCanvasElement>(null);
  const scrollRef   = useRef<HTMLDivElement>(null);
  const wrapperRef  = useRef<HTMLDivElement>(null);

  const [tooltip, setTooltip] = useState<{
    x: number;
    y: number;
    segment: EmotionSegment;
  } | null>(null);

  const segments      = data.segments;
  const totalDuration = segments.at(-1)?.end ?? 1;
  
  // Clamped to prevent browser InvalidStateError for long podcasts
  const canvasWidth   = Math.min(
    Math.max(MIN_CANVAS_WIDTH, PX_PER_SECOND * totalDuration), 
    MAX_CANVAS_WIDTH
  );

  const toX = useCallback(
    (t: number) => PAD_L + (t / totalDuration) * (canvasWidth - PAD_L - PAD_R),
    [totalDuration, canvasWidth]
  );
  const toY = useCallback(
    (v: number) => PAD_T + ((100 - v) / 100) * (CANVAS_H - PAD_T - PAD_B),
    []
  );

  const points = segments.map((seg) => ({
    time: (seg.start + seg.end) / 2,
    value: Math.round(seg.score * 100),
    segment: seg,
  }));

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    const dpr = window.devicePixelRatio || 1;
    canvas.width  = canvasWidth * dpr;
    canvas.height = CANVAS_H * dpr;
    canvas.style.width  = `${canvasWidth}px`;
    canvas.style.height = `${CANVAS_H}px`;

    const ctx = canvas.getContext("2d")!;
    ctx.scale(dpr, dpr);

    const cs            = getComputedStyle(document.documentElement);
    const textSecondary = cs.getPropertyValue("--color-text-secondary").trim()  || "rgba(128,128,128,0.6)";
    const borderColor   = cs.getPropertyValue("--color-border-tertiary").trim() || "rgba(128,128,128,0.15)";
    const textPrimary   = cs.getPropertyValue("--color-text-primary").trim()    || "#000";

    ctx.strokeStyle = borderColor;
    ctx.lineWidth = 0.5;
    for (let i = 0; i <= 4; i++) {
      const y = PAD_T + (i / 4) * (CANVAS_H - PAD_T - PAD_B);
      ctx.beginPath();
      ctx.moveTo(PAD_L, y);
      ctx.lineTo(canvasWidth - PAD_R, y);
      ctx.stroke();
    }

    ctx.beginPath();
    ctx.strokeStyle = textPrimary;
    ctx.lineWidth = 2;
    ctx.lineJoin = "round";
    points.forEach((pt, i) => {
      const x = toX(pt.time);
      const y = toY(pt.value);
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.stroke();

    ctx.fillStyle = textSecondary;
    ctx.font = "11px sans-serif";
    if (points.length > 0) {
      ctx.textAlign = "left";
      ctx.fillText(formatTime(points[0].segment.start), PAD_L, CANVAS_H - 6);
      ctx.textAlign = "right";
      ctx.fillText(formatTime(points[points.length - 1].segment.end), canvasWidth - PAD_R, CANVAS_H - 6);
    }

    points.forEach((pt) => {
      const x = toX(pt.time);
      const y = toY(pt.value);
      const color = getColor(pt.segment.dominant);

      ctx.beginPath();
      ctx.arc(x, y, 7, 0, Math.PI * 2);
      ctx.fillStyle = color;
      ctx.fill();

      ctx.beginPath();
      ctx.arc(x, y, 7, 0, Math.PI * 2);
      ctx.strokeStyle = borderColor;
      ctx.lineWidth = 1.5;
      ctx.stroke();
    });
  }, [points, canvasWidth, toX, toY]);

  useEffect(() => {
    const canvas = playheadRef.current;
    if (!canvas) return;

    const dpr = window.devicePixelRatio || 1;
    canvas.width  = canvasWidth * dpr;
    canvas.height = CANVAS_H * dpr;
    canvas.style.width  = `${canvasWidth}px`;
    canvas.style.height = `${CANVAS_H}px`;

    const ctx = canvas.getContext("2d")!;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, canvasWidth, CANVAS_H);

    const x = toX(currentTime);

    ctx.beginPath();
    ctx.strokeStyle = "rgba(255,255,255,0.85)";
    ctx.lineWidth = 2;
    ctx.setLineDash([4, 3]);
    ctx.moveTo(x, PAD_T);
    ctx.lineTo(x, CANVAS_H - PAD_B);
    ctx.stroke();
    ctx.setLineDash([]);

    ctx.beginPath();
    ctx.arc(x, PAD_T, 4, 0, Math.PI * 2);
    ctx.fillStyle = "rgba(255,255,255,0.9)";
    ctx.fill();
  }, [currentTime, canvasWidth, toX]);

  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll) return;
    const x      = toX(currentTime);
    const margin = 80;
    const left   = scroll.scrollLeft;
    const right  = left + scroll.clientWidth;
    if (x > right - margin) {
      scroll.scrollLeft = x - scroll.clientWidth + margin * 2;
    } else if (x < left + margin && left > 0) {
      scroll.scrollLeft = Math.max(0, x - margin * 2);
    }
  }, [currentTime, toX]);

  const handleClick = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!onSeek || !scrollRef.current) return;
    const rect      = scrollRef.current.getBoundingClientRect();
    const mouseX    = e.clientX - rect.left + scrollRef.current.scrollLeft;
    const clickTime = ((mouseX - PAD_L) / (canvasWidth - PAD_L - PAD_R)) * totalDuration;

    let closest = points[0];
    let minDist = Infinity;
    points.forEach((pt) => {
      const dist = Math.abs(toX(pt.time) - mouseX);
      if (dist < minDist) { minDist = dist; closest = pt; }
    });

    onSeek(minDist < 30
      ? closest.segment.start
      : Math.max(0, Math.min(totalDuration, clickTime))
    );
  };

  const handleMouseMove = (e: React.MouseEvent<HTMLDivElement>) => {
    const wrapper = wrapperRef.current;
    const scroll  = scrollRef.current;
    if (!wrapper || !scroll) return;

    const rect   = scroll.getBoundingClientRect();
    const mouseX = e.clientX - rect.left + scroll.scrollLeft;

    let closest = points[0];
    let minDist = Infinity;
    points.forEach((pt) => {
      const dist = Math.abs(toX(pt.time) - mouseX);
      if (dist < minDist) { minDist = dist; closest = pt; }
    });

    if (minDist < 30) {
      const wrapRect = wrapper.getBoundingClientRect();
      setTooltip({
        x: e.clientX - wrapRect.left,
        y: e.clientY - wrapRect.top,
        segment: closest.segment,
      });
    } else {
      setTooltip(null);
    }
  };

  return (
    <div className="w-full rounded-xl p-5">
      <p className="text-lg font-medium mb-4">Emotionchart</p>

      <div className="flex gap-4 min-w-0">
        <div className="flex flex-col gap-2 justify-around shrink-0">
          {ALL_EMOTIONS.map((em) => (
            <div key={em} className="flex items-center gap-2">
              <div
                className="rounded-sm shrink-0"
                style={{ width: 22, height: 14, background: getColor(em) }}
              />
              <span className="text-xs text-foreground-subtle" style={{ whiteSpace: "nowrap" }}>
                {em}
              </span>
            </div>
          ))}
        </div>

        <div ref={wrapperRef} className="flex-1 min-w-0 relative">
          <div
            ref={scrollRef}
            className="overflow-x-auto cursor-pointer"
            onClick={handleClick}
            onMouseMove={handleMouseMove}
            onMouseLeave={() => setTooltip(null)}
          >
            <div className="relative" style={{ width: canvasWidth, height: CANVAS_H }}>
              <canvas ref={canvasRef}   className="absolute top-0 left-0 pointer-events-none" />
              <canvas ref={playheadRef} className="absolute top-0 left-0 pointer-events-none" />
            </div>
          </div>

          {tooltip && (
            <div
              className="absolute pointer-events-none rounded-lg px-3 py-2 text-xs bg-background-raised border border-border text-foreground z-10"
              style={{ left: tooltip.x - 60, top: tooltip.y - 80, minWidth: 120 }}
            >
              <div className="flex items-center gap-2 mb-1">
                <div
                  className="rounded-sm shrink-0"
                  style={{ width: 10, height: 10, background: getColor(tooltip.segment.dominant) }}
                />
                <span className="font-medium">{tooltip.segment.dominant}</span>
              </div>
              <div className="text-foreground-subtle">
                {formatTime(tooltip.segment.start)} – {formatTime(tooltip.segment.end)}
              </div>
              <div className="text-foreground-subtle">
                Score: {Math.round(tooltip.segment.score * 100)}%
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

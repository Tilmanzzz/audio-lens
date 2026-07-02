"use client";

import { useState, useRef, useEffect, MutableRefObject } from "react";

interface AudioPlayerProps {
  src: string;
  onTimeUpdate?: (time: number) => void;
  seekRef?: MutableRefObject<((time: number) => void) | null>;
}

function formatTime(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = Math.floor(seconds % 60);
  return `${m}:${s.toString().padStart(2, "0")}`;
}

export default function AudioPlayer({ src, onTimeUpdate, seekRef }: AudioPlayerProps) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [isPlaying, setIsPlaying] = useState<boolean>(false);
  const [currentTime, setCurrentTime] = useState<number>(0);
  const [duration, setDuration] = useState<number>(0);

  const updateDuration = (audio: HTMLAudioElement) => {
    if (isFinite(audio.duration) && audio.duration > 0) {
      setDuration(audio.duration);
    }
  };

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;

    const handleTimeUpdate = () => {
      setCurrentTime(audio.currentTime);
      onTimeUpdate?.(audio.currentTime);
    };
    // loadedmetadata: normaler Fall
    const handleLoadedMetadata = () => updateDuration(audio);
    // durationchange: Fallback z.B. bei Streaming oder Cache
    const handleDurationChange = () => updateDuration(audio);
    const handleEnded = () => setIsPlaying(false);

    audio.addEventListener("timeupdate", handleTimeUpdate);
    audio.addEventListener("loadedmetadata", handleLoadedMetadata);
    audio.addEventListener("durationchange", handleDurationChange);
    audio.addEventListener("ended", handleEnded);

    // Falls Metadaten schon geladen sind (Cache), direkt auslesen
    if (audio.readyState >= 1) {
      updateDuration(audio);
    }

    return () => {
      audio.removeEventListener("timeupdate", handleTimeUpdate);
      audio.removeEventListener("loadedmetadata", handleLoadedMetadata);
      audio.removeEventListener("durationchange", handleDurationChange);
      audio.removeEventListener("ended", handleEnded);
    };
  }, [onTimeUpdate]);

  useEffect(() => {
    if (seekRef) {
      seekRef.current = (time: number) => {
        const audio = audioRef.current;
        if (!audio) return;
        audio.currentTime = time;
        setCurrentTime(time);
        if (!isPlaying) {
          audio.play();
          setIsPlaying(true);
        }
      };
    }
  }, [seekRef, isPlaying]);

  const togglePlay = (): void => {
    const audio = audioRef.current;
    if (!audio) return;
    if (isPlaying) {
      audio.pause();
    } else {
      audio.play();
    }
    setIsPlaying(!isPlaying);
  };

  const handleSeek = (e: React.ChangeEvent<HTMLInputElement>): void => {
    const audio = audioRef.current;
    if (!audio) return;
    const newTime = parseFloat(e.target.value);
    audio.currentTime = newTime;
    setCurrentTime(newTime);
    onTimeUpdate?.(newTime);
  };

  return (
    <div className="flex items-center gap-4 px-6 py-5 max-w-5xl rounded-xl bg-background-card border border-border">
      <audio ref={audioRef} src={src} preload="metadata" />

      <button
        onClick={togglePlay}
        aria-label={isPlaying ? "Pause" : "Play"}
        className="shrink-0 w-10 h-10 rounded-full bg-accent hover:bg-accent-hover flex items-center justify-center transition-colors cursor-pointer"
      >
        {isPlaying ? <PauseIcon /> : <PlayIcon />}
      </button>

      <div className="flex flex-col gap-1.5 flex-1">
        <input
          type="range"
          min={0}
          max={duration || 0}
          step={0.1}
          value={currentTime}
          onChange={handleSeek}
          aria-label="Seek"
          className="w-full cursor-pointer accent-accent"
        />
        <div className="flex justify-between text-xs text-foreground-subtle">
          <span>{formatTime(currentTime)}</span>
          <span>{duration > 0 ? formatTime(duration) : "--:--"}</span>
        </div>
      </div>
    </div>
  );
}

function PlayIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="var(--background)">
      <polygon points="5 3 19 12 5 21" />
    </svg>
  );
}

function PauseIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="var(--background)">
      <rect x="5" y="4" width="4" height="16" />
      <rect x="15" y="4" width="4" height="16" />
    </svg>
  );
}

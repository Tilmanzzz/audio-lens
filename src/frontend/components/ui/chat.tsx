"use client";

import { useState, useRef, useEffect } from "react";
import ReactMarkdown from "react-markdown";

interface Message {
  role: "user" | "assistant";
  content: string;
  isError?: boolean;
}

interface RagChatProps {
  episodeId: string;
  placeholder?: string;
  onSeek?: (time: number) => void;
}

function parseTimestamp(ts: string): number {
  const parts = ts.split(":").map(Number);
  if (parts.length === 3) return parts[0] * 3600 + parts[1] * 60 + parts[2];
  return parts[0] * 60 + parts[1];
}

// Converts [01:30] into a markdown link: [01:30](timestamp:01:30)
function preprocessTimestamps(text: string): string {
  const TIMESTAMP_RE = /\[(\d{1,2}:\d{2}(?::\d{2})?)\]/g;
  return text.replace(TIMESTAMP_RE, "[$1](#timestamp:$1)"); 
}

export default function RagChat({
  episodeId,
  placeholder = "Ask something about the episode..",
  onSeek,
}: RagChatProps) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState("");
  const [isLoading, setIsLoading] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (messages.length === 0) return;
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages, isLoading]);

  const handleSubmit = async () => {
    const query = input.trim();
    if (!query || isLoading) return;

    const history: { role: string; content: string }[] = [];
    for (let i = 0; i + 1 < messages.length; i += 2) {
      if (!messages[i + 1].isError) {
        history.push({ role: messages[i].role, content: messages[i].content });
        history.push({ role: messages[i + 1].role, content: messages[i + 1].content });
      }
    }
    setMessages((prev) => [...prev, { role: "user", content: query }]);
    setInput("");
    setIsLoading(true);

    try {
      const backendUrl = process.env.NEXT_PUBLIC_BACKEND_URL ?? "http://localhost:8080";
      const res = await fetch(`${backendUrl}/api/v1/episodes/${episodeId}/chat`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ question: query, history }),
      });

      if (!res.ok) throw new Error(`Backend error ${res.status}`);

      const data = (await res.json()) as { answer: string };
      setMessages((prev) => [...prev, { role: "assistant", content: data.answer }]);
    } catch {
      setMessages((prev) => [
        ...prev,
        { role: "assistant", content: "Error searching for an answer.", isError: true },
      ]);
    } finally {
      setIsLoading(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      handleSubmit();
    }
  };

  const handleInput = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setInput(e.target.value);
    const el = e.target;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  };

  const canSend = input.trim() && !isLoading;

  return (
    <div className="flex flex-col h-full min-h-64 bg-background-card border border-border rounded-xl overflow-hidden">
      {/* Nachrichtenverlauf */}
      <div className="flex-1 overflow-y-auto px-4 py-4 flex flex-col gap-3">
        {messages.length === 0 && (
          <div className="flex flex-col items-center justify-center h-full gap-2 text-center py-8"></div>
        )}

        {messages.map((msg, index) => (
          <div
            key={index}
            className={`flex ${msg.role === "user" ? "justify-end" : "justify-start"}`}
          >
            <div
              className={`max-w-[80%] rounded-xl px-4 py-2.5 text-sm leading-relaxed
                ${
                  msg.role === "user"
                    ? "bg-primary text-foreground rounded-br-sm"
                    : "bg-background-raised text-foreground border border-border rounded-bl-sm"
                }`}
            >
              {msg.role === "assistant" ? (
                <ReactMarkdown
                  components={{
                    // Map generic text elements to preserve formatting since Tailwind resets them
                    p: ({ children }) => <p className="mb-2 last:mb-0">{children}</p>,
                    ul: ({ children }) => <ul className="list-disc pl-4 mb-2">{children}</ul>,
                    ol: ({ children }) => <ol className="list-decimal pl-4 mb-2">{children}</ol>,
                    strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
                    code: ({ children }) => (
                      <code className="bg-background-card border border-border rounded px-1 py-0.5 text-xs">
                        {children}
                      </code>
                    ),
                    // Intercept links to render timestamps as buttons
                    a: ({ href, children }) => {
                      if (href?.startsWith("#timestamp:")) {
                        const ts = href.replace("#timestamp:", "");
                        const seconds = parseTimestamp(ts);
                        return (
                          <button
                            type="button"
                            onClick={(e) => {
                              e.preventDefault(); // Ensures the browser doesn't attempt any default routing
                              onSeek?.(seconds);
                            }}
                            className="inline text-accent hover:text-accent-hover underline underline-offset-2 cursor-pointer font-medium"
                          >
                            {children}
                          </button>
                        );
                      }
                      
                      // Render standard markdown links normally
                      return (
                        <a
                          href={href}
                          target="_blank"
                          rel="noreferrer"
                          className="underline text-accent"
                        >
                          {children}
                        </a>
                      );
                    },
                  }}
                >
                  {preprocessTimestamps(msg.content)}
                </ReactMarkdown>
              ) : (
                msg.content
              )}
            </div>
          </div>
        ))}

        {/* Lade-Dots während Backend antwortet */}
        {isLoading && (
          <div className="flex justify-start">
            <div className="bg-background-raised border border-border rounded-xl rounded-bl-sm px-4 py-3 flex gap-1.5 items-center">
              <span className="w-1.5 h-1.5 rounded-full bg-foreground-subtle animate-bounce [animation-delay:0ms]" />
              <span className="w-1.5 h-1.5 rounded-full bg-foreground-subtle animate-bounce [animation-delay:150ms]" />
              <span className="w-1.5 h-1.5 rounded-full bg-foreground-subtle animate-bounce [animation-delay:300ms]" />
            </div>
          </div>
        )}

        <div ref={bottomRef} />
      </div>

      {/* Eingabefeld */}
      <div className="border-t border-border px-3 py-3 flex gap-2 items-end bg-background-card">
        <textarea
          ref={inputRef}
          value={input}
          onChange={handleInput}
          onKeyDown={handleKeyDown}
          placeholder={placeholder}
          rows={1}
          className="flex-1 resize-none bg-background-raised border border-border rounded-lg px-3 py-2 text-sm text-foreground placeholder:text-foreground-subtle focus:outline-none focus:border-primary transition-colors max-h-32 overflow-y-auto disabled:opacity-50"
        />
        <button
          onClick={handleSubmit}
          disabled={!canSend}
          className="shrink-0 w-9 h-9 rounded-lg bg-primary hover:bg-primary-hover disabled:opacity-40 disabled:cursor-not-allowed flex items-center justify-center transition-colors cursor-pointer"
          aria-label="Senden"
        >
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="text-foreground rotate-90">
            <line x1="12" y1="19" x2="12" y2="5" />
            <polyline points="5 12 12 5 19 12" />
          </svg>
        </button>
      </div>
    </div>
  );
}

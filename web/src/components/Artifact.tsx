import { useMemo } from "react";
import { Download, FileText, Image as ImageIcon, FileDiff } from "lucide-react";
import type { Attachment } from "../gen/nexus/v1/common_pb";
import { cx, formatBytes } from "../lib/format";
import { hubBaseUrl } from "../lib/api";

function toDataUrl(bytes: Uint8Array, mime: string): string {
  let bin = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) bin += String.fromCharCode(...bytes.subarray(i, i + chunk));
  return `data:${mime};base64,${btoa(bin)}`;
}

function useObjectUrl(att: Attachment): string | undefined {
  // Inline artifacts are small (the Hub issues blob URLs for big ones), so a data: URL is fine and
  // needs no lifecycle management.
  const url = useMemo(() => (att.data.length ? toDataUrl(att.data, att.mimeType || "application/octet-stream") : undefined), [att]);
  if (!att.data.length && att.url) {
    // Blob URLs issued by the Hub are relative to it.
    return att.url.startsWith("http") ? att.url : `${hubBaseUrl()}${att.url.startsWith("/") ? "" : "/"}${att.url}`;
  }
  return url;
}

export function DiffView({ text, className }: { text: string; className?: string }) {
  const lines = useMemo(() => text.split("\n"), [text]);
  return (
    <pre className={cx("max-h-80 overflow-auto rounded-lg border border-border bg-surface-2 py-2 font-mono text-[11.5px] leading-[1.55] scrollbar-thin", className)}>
      {lines.map((l, i) => (
        <div
          key={i}
          className={cx(
            "px-3 whitespace-pre",
            l.startsWith("+") && !l.startsWith("+++") && "bg-ok-soft text-ok",
            l.startsWith("-") && !l.startsWith("---") && "bg-danger-soft text-danger",
            l.startsWith("@@") && "text-info",
            (l.startsWith("diff ") || l.startsWith("+++") || l.startsWith("---")) && "font-semibold text-fg-2",
          )}
        >
          {l || " "}
        </div>
      ))}
    </pre>
  );
}

export function ArtifactView({ att }: { att: Attachment }) {
  const url = useObjectUrl(att);
  const isImage = att.mimeType.startsWith("image/");
  const isDiff = att.mimeType.includes("diff") || att.name.endsWith(".diff") || att.name.endsWith(".patch");
  const isText = isDiff || att.mimeType.startsWith("text/") || att.mimeType === "application/json";
  const text = useMemo(() => (isText && att.data.length ? new TextDecoder().decode(att.data) : ""), [att, isText]);
  const Icon = isImage ? ImageIcon : isDiff ? FileDiff : FileText;
  return (
    <div className="overflow-hidden rounded-xl border border-border bg-surface">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2 text-xs">
        <Icon className="size-3.5 text-muted" />
        <span className="truncate font-medium text-fg">{att.name || "artifact"}</span>
        <span className="text-muted">{formatBytes(att.sizeBytes || BigInt(att.data.length))}</span>
        <span className="flex-1" />
        {url && (
          <a href={url} download={att.name} aria-label={`Download ${att.name}`} className="inline-flex min-h-8 min-w-8 items-center justify-center gap-1 rounded-md px-1.5 py-0.5 text-muted hover:bg-surface-2 hover:text-fg pointer-coarse:size-11">
            <Download className="size-3.5" />
          </a>
        )}
      </div>
      {isImage && url && (
        <div className="bg-surface-2 p-3">
          <img src={url} alt={att.name} className="mx-auto max-h-72 rounded-lg shadow-card" />
        </div>
      )}
      {isDiff && text && <DiffView text={text} className="m-2 rounded-lg" />}
      {!isDiff && isText && text && (
        <pre className="max-h-64 overflow-auto p-3 font-mono text-xs text-fg-2 scrollbar-thin">{text}</pre>
      )}
    </div>
  );
}

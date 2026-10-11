import { memo } from "react";
import ReactMarkdown, { defaultUrlTransform, type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Link } from "react-router";
import { cx } from "../lib/format";

const WIKILINK = /\[\[([^\]|#]+)(#[^\]|]*)?(?:\|([^\]]+))?\]\]/g;

/** Turn [[Target|alias]] into markdown links with a private scheme the renderer understands. */
export function preprocessWikilinks(md: string): string {
  // Leave fenced code blocks alone.
  return md
    .split(/(```[\s\S]*?```)/g)
    .map((chunk, i) =>
      i % 2 === 1
        ? chunk
        : chunk.replace(WIKILINK, (_m, target: string, _anchor: string | undefined, alias: string | undefined) => {
            const label = (alias ?? target).trim().replace(/[[\]]/g, "");
            return `[${label}](wikilink:${encodeURIComponent(target.trim())})`;
          }),
    )
    .join("");
}

function urlTransform(url: string): string {
  if (url.startsWith("wikilink:")) return url;
  return defaultUrlTransform(url);
}

export interface MarkdownProps {
  children: string;
  className?: string;
  /** Known note titles/ids (lowercase) to style unresolved links. */
  known?: Set<string>;
  /** Override where wikilinks go (default: /vault?note=<target>). */
  linkTo?: (target: string) => string;
}

export const Markdown = memo(function Markdown({ children, className, known, linkTo }: MarkdownProps) {
  const components: Components = {
    a({ href, children: kids, ...rest }) {
      if (href?.startsWith("wikilink:")) {
        const target = decodeURIComponent(href.slice("wikilink:".length));
        const missing = known ? !known.has(target.toLowerCase()) : false;
        return (
          <Link
            to={linkTo ? linkTo(target) : `/vault?note=${encodeURIComponent(target)}`}
            className={cx("wikilink", missing && "missing")}
            title={missing ? `${target} (no note yet)` : target}
          >
            {kids}
          </Link>
        );
      }
      return (
        <a href={href} target="_blank" rel="noreferrer noopener" {...rest}>
          {kids}
        </a>
      );
    },
  };
  return (
    <div className={cx("prose-nexus", className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={urlTransform} components={components}>
        {preprocessWikilinks(children)}
      </ReactMarkdown>
    </div>
  );
});

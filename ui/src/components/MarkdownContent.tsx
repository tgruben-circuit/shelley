import React, { useMemo } from "react";
import { Marked } from "marked";
import DOMPurify from "dompurify";

interface MarkdownContentProps {
  text: string;
}

// Create a dedicated marked instance to avoid mutating the global singleton
const markedInstance = new Marked({
  gfm: true,
  breaks: true,
});

// Make all links open in new tabs, and restrict <input> to checkboxes only.
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
  if (node.tagName === "INPUT" && node.getAttribute("type") !== "checkbox") {
    node.remove();
  }
});

// Rewrite <confidence level="high|medium|low">body</confidence> blocks into
// styled callouts before markdown parsing. Anything that doesn't match the
// strict pattern passes through untouched.
const CONFIDENCE_RE = /<confidence\s+level="(high|medium|low)">([\s\S]*?)<\/confidence>/gi;
function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
function renderConfidenceBlocks(text: string): string {
  return text.replace(CONFIDENCE_RE, (_m, level: string, body: string) => {
    const lvl = level.toLowerCase();
    const label = lvl.charAt(0).toUpperCase() + lvl.slice(1);
    const safeBody = escapeHtml(body.trim());
    return `<div class="percy-confidence percy-confidence-${lvl}"><span class="percy-confidence-label">${label} confidence</span><span class="percy-confidence-body">${safeBody}</span></div>`;
  });
}

function MarkdownContent({ text }: MarkdownContentProps) {
  const html = useMemo(() => {
    const pre = renderConfidenceBlocks(text);
    const raw = markedInstance.parse(pre, { async: false }) as string;
    return DOMPurify.sanitize(raw, {
      ALLOWED_TAGS: [
        "p", "br", "strong", "em", "code", "pre", "blockquote",
        "ul", "ol", "li", "a", "h1", "h2", "h3", "h4", "h5", "h6",
        "hr", "table", "thead", "tbody", "tr", "th", "td",
        "del", "input", "span", "div",
      ],
      ALLOWED_ATTR: ["href", "target", "rel", "type", "checked", "disabled", "class"],
    });
  }, [text]);

  return (
    <div className="markdown-content break-words" dangerouslySetInnerHTML={{ __html: html }} />
  );
}

export default MarkdownContent;

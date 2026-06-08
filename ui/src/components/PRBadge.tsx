import React from "react";
import { PRSummary } from "../types";

interface PRBadgeProps {
  pr: PRSummary;
  onClick?: () => void;
}

// Maps a PR to a state label and CSS modifier used for coloring.
function prStatus(pr: PRSummary): { label: string; modifier: string } {
  if (pr.is_draft && pr.state === "OPEN") {
    return { label: "Draft", modifier: "draft" };
  }
  switch (pr.state) {
    case "MERGED":
      return { label: "Merged", modifier: "merged" };
    case "CLOSED":
      return { label: "Closed", modifier: "closed" };
    default:
      // Open PR: color by review decision.
      if (pr.review_decision === "APPROVED") return { label: "Approved", modifier: "approved" };
      if (pr.review_decision === "CHANGES_REQUESTED")
        return { label: "Changes", modifier: "changes" };
      return { label: "Open", modifier: "open" };
  }
}

function PRBadge({ pr, onClick }: PRBadgeProps) {
  const { label, modifier } = prStatus(pr);
  const title = `PR #${pr.number} — ${label}${
    pr.unresolved_count > 0 ? ` · ${pr.unresolved_count} unresolved` : ""
  }`;
  return (
    <button
      type="button"
      className={`pr-badge pr-badge--${modifier}`}
      title={title}
      onClick={(e) => {
        e.stopPropagation();
        onClick?.();
      }}
    >
      <span className="pr-badge-num">#{pr.number}</span>
      <span className="pr-badge-label">{label}</span>
      {pr.unresolved_count > 0 && (
        <span className="pr-badge-count" aria-label={`${pr.unresolved_count} unresolved comments`}>
          {pr.unresolved_count}
        </span>
      )}
    </button>
  );
}

export default PRBadge;

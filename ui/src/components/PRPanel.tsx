import React, { useEffect, useState } from "react";
import { PRDetail, PRResponse, PRComment, ReviewThread } from "../types";
import { api } from "../services/api";

interface PRPanelProps {
  conversationId: string | null;
  isOpen: boolean;
  onClose: () => void;
}

function reviewStateLabel(state: string): string {
  switch (state) {
    case "APPROVED":
      return "approved";
    case "CHANGES_REQUESTED":
      return "requested changes";
    case "DISMISSED":
      return "dismissed";
    default:
      return "commented";
  }
}

function CommentItem({ comment }: { comment: PRComment }) {
  return (
    <div className="pr-comment">
      <div className="pr-comment-head">
        <span className="pr-comment-author">{comment.author || "unknown"}</span>
        {comment.created_at && (
          <span className="pr-comment-date">{comment.created_at.slice(0, 10)}</span>
        )}
      </div>
      <div className="pr-comment-body">{comment.body}</div>
    </div>
  );
}

// CommentBox is a textarea + submit used for thread replies and PR comments.
function CommentBox({
  placeholder,
  submitLabel,
  disabled,
  onSubmit,
}: {
  placeholder: string;
  submitLabel: string;
  disabled?: boolean;
  onSubmit: (body: string) => Promise<boolean>;
}) {
  const [text, setText] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const submit = async () => {
    const body = text.trim();
    if (!body || submitting) return;
    setSubmitting(true);
    const ok = await onSubmit(body);
    setSubmitting(false);
    if (ok) setText("");
  };

  return (
    <div className="pr-commentbox">
      <textarea
        className="pr-commentbox-input"
        placeholder={placeholder}
        value={text}
        disabled={disabled || submitting}
        onChange={(e) => setText(e.target.value)}
        rows={2}
      />
      <div className="pr-commentbox-actions">
        <span className="pr-commentbox-hint">Posts publicly to GitHub</span>
        <button
          type="button"
          className="btn-secondary pr-commentbox-submit"
          disabled={disabled || submitting || text.trim() === ""}
          onClick={submit}
        >
          {submitting ? "Posting…" : submitLabel}
        </button>
      </div>
    </div>
  );
}

function ThreadItem({
  thread,
  writable,
  onReply,
  onResolve,
}: {
  thread: ReviewThread;
  writable: boolean;
  onReply: (threadId: string, body: string) => Promise<boolean>;
  onResolve: (threadId: string, resolved: boolean) => Promise<boolean>;
}) {
  const [confirming, setConfirming] = useState(false);
  const [resolving, setResolving] = useState(false);

  const toggleResolve = async () => {
    setResolving(true);
    await onResolve(thread.id, !thread.is_resolved);
    setResolving(false);
    setConfirming(false);
  };

  return (
    <div className={`pr-thread ${thread.is_resolved ? "pr-thread--resolved" : ""}`}>
      <div className="pr-thread-head">
        <span className="pr-thread-loc">
          {thread.path}
          {thread.line ? `:${thread.line}` : ""}
        </span>
        <span
          className={`pr-thread-chip ${
            thread.is_resolved ? "pr-thread-chip--resolved" : "pr-thread-chip--open"
          }`}
        >
          {thread.is_resolved ? "Resolved" : "Unresolved"}
        </span>
        {thread.is_outdated && <span className="pr-thread-chip">Outdated</span>}
      </div>
      {(thread.comments ?? []).map((c) => (
        <CommentItem key={c.id} comment={c} />
      ))}
      {writable && (
        <>
          <CommentBox
            placeholder="Reply…"
            submitLabel="Reply"
            onSubmit={(body) => onReply(thread.id, body)}
          />
          <div className="pr-thread-resolve">
            {confirming ? (
              <>
                <button
                  type="button"
                  className="btn-secondary"
                  disabled={resolving}
                  onClick={toggleResolve}
                >
                  {resolving
                    ? "Working…"
                    : `Confirm ${thread.is_resolved ? "unresolve" : "resolve"}`}
                </button>
                <button
                  type="button"
                  className="btn-link"
                  disabled={resolving}
                  onClick={() => setConfirming(false)}
                >
                  Cancel
                </button>
              </>
            ) : (
              <button type="button" className="btn-link" onClick={() => setConfirming(true)}>
                {thread.is_resolved ? "Unresolve" : "Resolve"}
              </button>
            )}
          </div>
        </>
      )}
    </div>
  );
}

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: React.ReactNode;
}) {
  if (count === 0) return null;
  return (
    <div className="pr-section">
      <h3 className="pr-section-title">
        {title} <span className="pr-section-count">{count}</span>
      </h3>
      {children}
    </div>
  );
}

function PRPanel({ conversationId, isOpen, onClose }: PRPanelProps) {
  const [detail, setDetail] = useState<PRDetail | null>(null);
  const [error, setError] = useState<string>("");
  const [actionError, setActionError] = useState<string>("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!isOpen || !conversationId) return;
    let cancelled = false;
    setLoading(true);
    setError("");
    setActionError("");
    setDetail(null);
    api
      .getPR(conversationId)
      .then((resp) => {
        if (cancelled) return;
        setDetail(resp.detail);
        setError(resp.error ?? "");
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [isOpen, conversationId]);

  // applyResult updates the panel from a mutation response and reports success.
  const applyResult = (resp: PRResponse): boolean => {
    if (resp.error) {
      setActionError(resp.error);
      return false;
    }
    setActionError("");
    if (resp.detail) setDetail(resp.detail);
    return true;
  };

  const runAction = async (p: Promise<PRResponse>): Promise<boolean> => {
    try {
      return applyResult(await p);
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e));
      return false;
    }
  };

  const handleReply = (threadId: string, body: string) =>
    conversationId
      ? runAction(api.replyToPRThread(conversationId, threadId, body))
      : Promise.resolve(false);
  const handleResolve = (threadId: string, resolved: boolean) =>
    conversationId
      ? runAction(api.resolvePRThread(conversationId, threadId, resolved))
      : Promise.resolve(false);
  const handleComment = (body: string) =>
    conversationId ? runAction(api.commentOnPR(conversationId, body)) : Promise.resolve(false);

  if (!isOpen) return null;

  const reviews = detail?.reviews ?? [];
  const conversation = detail?.conversation ?? [];
  const threads = detail?.threads ?? [];
  const writable = detail?.state === "OPEN";

  return (
    <>
      <div className="pr-panel-backdrop" onClick={onClose} />
      <aside className="pr-panel" role="dialog" aria-label="Pull request review">
        <div className="pr-panel-header">
          <div className="pr-panel-title">
            {detail ? (
              <a href={detail.url} target="_blank" rel="noreferrer">
                #{detail.number} {detail.title}
              </a>
            ) : (
              "Pull Request"
            )}
          </div>
          <button type="button" className="btn-icon-sm" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>

        <div className="pr-panel-body">
          {loading && <div className="pr-panel-status">Loading…</div>}
          {!loading && error && <div className="pr-panel-status pr-panel-error">{error}</div>}
          {!loading && !error && !detail && (
            <div className="pr-panel-status">No open pull request for this branch.</div>
          )}
          {actionError && <div className="pr-panel-status pr-panel-error">{actionError}</div>}

          {detail && (
            <>
              {!writable && (
                <div className="pr-panel-note">
                  This pull request is {detail.state.toLowerCase()} — replies and resolving are
                  disabled.
                </div>
              )}

              <Section title="Reviews" count={reviews.length}>
                {reviews.map((r) => (
                  <div key={r.id} className="pr-comment">
                    <div className="pr-comment-head">
                      <span className="pr-comment-author">{r.author || "unknown"}</span>
                      <span className="pr-review-state">{reviewStateLabel(r.state)}</span>
                    </div>
                    {r.body && <div className="pr-comment-body">{r.body}</div>}
                  </div>
                ))}
              </Section>

              <Section title="Inline comments" count={threads.length}>
                {threads.map((t) => (
                  <ThreadItem
                    key={t.id}
                    thread={t}
                    writable={writable}
                    onReply={handleReply}
                    onResolve={handleResolve}
                  />
                ))}
              </Section>

              <Section title="Conversation" count={conversation.length}>
                {conversation.map((c) => (
                  <CommentItem key={c.id} comment={c} />
                ))}
              </Section>

              {writable && (
                <div className="pr-section">
                  <h3 className="pr-section-title">Add a comment</h3>
                  <CommentBox
                    placeholder="Leave a comment on this PR…"
                    submitLabel="Comment"
                    onSubmit={handleComment}
                  />
                </div>
              )}
            </>
          )}
        </div>
      </aside>
    </>
  );
}

export default PRPanel;

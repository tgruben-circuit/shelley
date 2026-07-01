import React, { useState, useEffect } from "react";
import { Conversation, ConversationWithState } from "../types";
import { api } from "../services/api";
import Modal from "./Modal";
import SkillsList from "./SkillsList";
import PRBadge from "./PRBadge";

interface ConversationDrawerProps {
  isOpen: boolean;
  isCollapsed: boolean;
  onClose: () => void;
  onToggleCollapse: () => void;
  conversations: ConversationWithState[];
  currentConversationId: string | null;
  viewedConversation?: Conversation | null; // The currently viewed conversation (may be a subagent)
  onSelectConversation: (conversation: Conversation) => void;
  onNewConversation: () => void;
  onConversationArchived?: (id: string) => void;
  onConversationUnarchived?: (conversation: Conversation) => void;
  onConversationRenamed?: (conversation: Conversation) => void;
  subagentUpdate?: Conversation | null; // When a subagent is created/updated
  subagentStateUpdate?: { conversation_id: string; working: boolean } | null; // When a subagent's working state changes
  onShowCostDashboard?: () => void;
  onOpenPRPanel?: (conversationId: string) => void; // Open the PR review panel for a conversation
}

function ConversationDrawer({
  isOpen,
  isCollapsed,
  onClose,
  onToggleCollapse,
  conversations,
  currentConversationId,
  viewedConversation,
  onSelectConversation,
  onNewConversation,
  onConversationArchived,
  onConversationUnarchived,
  onConversationRenamed,
  subagentUpdate,
  subagentStateUpdate,
  onShowCostDashboard,
  onOpenPRPanel,
}: ConversationDrawerProps) {
  const [showArchived, setShowArchived] = useState(false);
  const [showSkillsModal, setShowSkillsModal] = useState(false);
  const [archivedConversations, setArchivedConversations] = useState<Conversation[]>([]);
  const [loadingArchived, setLoadingArchived] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingSlug, setEditingSlug] = useState("");
  const [tagEditingId, setTagEditingId] = useState<string | null>(null);
  const [tagInput, setTagInput] = useState("");
  const tagInputRef = React.useRef<HTMLInputElement>(null);
  const [subagents, setSubagents] = useState<Record<string, ConversationWithState[]>>({});
  const [expandedSubagents, setExpandedSubagents] = useState<Set<string>>(new Set());
  const renameInputRef = React.useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (showArchived && archivedConversations.length === 0) {
      loadArchivedConversations();
    }
  }, [showArchived]);

  // Load subagents for the current conversation (or parent if viewing a subagent)
  useEffect(() => {
    if (!showArchived && currentConversationId) {
      // If viewing a subagent, also load and expand the parent's subagents
      const parentId = viewedConversation?.parent_conversation_id;
      if (parentId) {
        loadSubagents(parentId);
        setExpandedSubagents((prev) => new Set([...prev, parentId]));
      } else {
        loadSubagents(currentConversationId);
        setExpandedSubagents((prev) => new Set([...prev, currentConversationId]));
      }
    }
  }, [currentConversationId, viewedConversation, showArchived]);

  // Handle real-time subagent updates
  useEffect(() => {
    if (subagentUpdate && subagentUpdate.parent_conversation_id) {
      const parentId = subagentUpdate.parent_conversation_id;
      setSubagents((prev) => {
        const existing = prev[parentId] || [];
        // Check if this subagent already exists
        const existingIndex = existing.findIndex(
          (s) => s.conversation_id === subagentUpdate.conversation_id,
        );
        if (existingIndex >= 0) {
          // Update existing, preserving working state
          const updated = [...existing];
          updated[existingIndex] = { ...subagentUpdate, working: existing[existingIndex].working };
          return { ...prev, [parentId]: updated };
        } else {
          // Add new subagent (not working by default)
          return { ...prev, [parentId]: [...existing, { ...subagentUpdate, working: false }] };
        }
      });
      // Auto-expand parent only if it's the currently selected conversation
      if (parentId === currentConversationId) {
        setExpandedSubagents((prev) => new Set([...prev, parentId]));
      }
    }
  }, [subagentUpdate, currentConversationId]);

  // Handle subagent working state updates
  useEffect(() => {
    if (subagentStateUpdate) {
      setSubagents((prev) => {
        // Find which parent contains this subagent
        for (const [parentId, subs] of Object.entries(prev)) {
          const subIndex = subs.findIndex(
            (s) => s.conversation_id === subagentStateUpdate.conversation_id,
          );
          if (subIndex >= 0) {
            const updated = [...subs];
            updated[subIndex] = { ...updated[subIndex], working: subagentStateUpdate.working };
            return { ...prev, [parentId]: updated };
          }
        }
        return prev;
      });
    }
  }, [subagentStateUpdate]);

  const loadSubagents = async (conversationId: string) => {
    // Skip if already loaded
    if (subagents[conversationId]) return;
    try {
      const subs = await api.getSubagents(conversationId);
      if (subs && subs.length > 0) {
        // Add working: false to each subagent
        const subsWithState = subs.map((s) => ({ ...s, working: false }));
        setSubagents((prev) => ({ ...prev, [conversationId]: subsWithState }));
      }
    } catch (err) {
      console.error("Failed to load subagents:", err);
    }
  };

  const toggleSubagents = (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    setExpandedSubagents((prev) => {
      const next = new Set(prev);
      if (next.has(conversationId)) {
        next.delete(conversationId);
      } else {
        next.add(conversationId);
        // Load subagents if not already loaded
        loadSubagents(conversationId);
      }
      return next;
    });
  };

  const loadArchivedConversations = async () => {
    setLoadingArchived(true);
    try {
      const archived = await api.getArchivedConversations();
      setArchivedConversations(archived);
    } catch (err) {
      console.error("Failed to load archived conversations:", err);
    } finally {
      setLoadingArchived(false);
    }
  };

  const formatDate = (timestamp: string) => {
    const date = new Date(timestamp);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));

    if (diffDays === 0) {
      return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    } else if (diffDays === 1) {
      return "Yesterday";
    } else if (diffDays < 7) {
      return `${diffDays} days ago`;
    } else {
      return date.toLocaleDateString();
    }
  };

  // Format cwd with ~ for home directory (display only)
  const formatCwdForDisplay = (cwd: string | null | undefined): string | null => {
    if (!cwd) return null;
    const homeDir = window.__PERCY_INIT__?.home_dir;
    if (homeDir && cwd === homeDir) {
      return "~";
    }
    if (homeDir && cwd.startsWith(homeDir + "/")) {
      return "~" + cwd.slice(homeDir.length);
    }
    return cwd;
  };

  const getConversationPreview = (conversation: Conversation) => {
    if (conversation.slug) {
      return conversation.slug;
    }
    // Show full conversation ID
    return conversation.conversation_id;
  };

  const handleArchive = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    try {
      await api.archiveConversation(conversationId);
      onConversationArchived?.(conversationId);
      // Refresh archived list if viewing
      if (showArchived) {
        loadArchivedConversations();
      }
    } catch (err) {
      console.error("Failed to archive conversation:", err);
    }
  };

  const handleUnarchive = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    try {
      const conversation = await api.unarchiveConversation(conversationId);
      setArchivedConversations((prev) => prev.filter((c) => c.conversation_id !== conversationId));
      onConversationUnarchived?.(conversation);
    } catch (err) {
      console.error("Failed to unarchive conversation:", err);
    }
  };

  const handleDelete = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    if (!confirm("Are you sure you want to permanently delete this conversation?")) {
      return;
    }
    try {
      await api.deleteConversation(conversationId);
      setArchivedConversations((prev) => prev.filter((c) => c.conversation_id !== conversationId));
    } catch (err) {
      console.error("Failed to delete conversation:", err);
    }
  };

  // Sanitize slug: lowercase, alphanumeric and hyphens only, max 60 chars
  const sanitizeSlug = (input: string): string => {
    return input
      .toLowerCase()
      .replace(/[\s_]+/g, "-")
      .replace(/[^a-z0-9-]+/g, "")
      .replace(/-+/g, "-")
      .replace(/^-|-$/g, "")
      .slice(0, 60)
      .replace(/-$/g, "");
  };

  const handleStartRename = (e: React.MouseEvent, conversation: Conversation) => {
    e.stopPropagation();
    setEditingId(conversation.conversation_id);
    setEditingSlug(conversation.slug || "");
    // Select all text after render
    setTimeout(() => renameInputRef.current?.select(), 0);
  };

  const handleRename = async (conversationId: string) => {
    const sanitized = sanitizeSlug(editingSlug);
    if (!sanitized) {
      setEditingId(null);
      return;
    }

    // Check for uniqueness against current conversations
    const isDuplicate = [...conversations, ...archivedConversations].some(
      (c) => c.slug === sanitized && c.conversation_id !== conversationId,
    );
    if (isDuplicate) {
      alert("A conversation with this name already exists");
      return;
    }

    try {
      const updated = await api.renameConversation(conversationId, sanitized);
      onConversationRenamed?.(updated);
      setEditingId(null);
    } catch (err) {
      console.error("Failed to rename conversation:", err);
    }
  };

  // Tags are stored on the conversation as a JSON array string.
  const parseTags = (conversation: Conversation): string[] => {
    try {
      const parsed = JSON.parse((conversation as Conversation).tags || "[]");
      return Array.isArray(parsed) ? parsed.filter((t): t is string => typeof t === "string") : [];
    } catch {
      return [];
    }
  };

  const handleStartTagEdit = (e: React.MouseEvent, conversation: Conversation) => {
    e.stopPropagation();
    setTagEditingId(conversation.conversation_id);
    setTagInput("");
    setTimeout(() => tagInputRef.current?.focus(), 0);
  };

  const saveTags = async (conversation: Conversation, tags: string[]) => {
    try {
      const updated = await api.updateConversationTags(conversation.conversation_id, tags);
      onConversationRenamed?.(updated);
    } catch (err) {
      console.error("Failed to update conversation tags:", err);
    }
  };

  const handleAddTag = async (conversation: Conversation) => {
    const tag = tagInput.trim();
    if (!tag) return;
    const existing = parseTags(conversation);
    if (!existing.includes(tag)) {
      await saveTags(conversation, [...existing, tag]);
    }
    setTagInput("");
  };

  const handleRemoveTag = async (e: React.MouseEvent, conversation: Conversation, tag: string) => {
    e.stopPropagation();
    await saveTags(
      conversation,
      parseTags(conversation).filter((t) => t !== tag),
    );
  };

  const handleTagKeyDown = (e: React.KeyboardEvent, conversation: Conversation) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "Enter") {
      e.preventDefault();
      handleAddTag(conversation);
    } else if (e.key === "Escape") {
      setTagEditingId(null);
      setTagInput("");
    }
  };

  const handleRenameKeyDown = (e: React.KeyboardEvent, conversationId: string) => {
    // Don't submit while IME is composing (e.g., converting Japanese hiragana to kanji)
    if (e.nativeEvent.isComposing) {
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      handleRename(conversationId);
    } else if (e.key === "Escape") {
      setEditingId(null);
    }
  };

  const displayedConversations = showArchived ? archivedConversations : conversations;

  return (
    <>
      {/* Drawer */}
      <div className={`drawer ${isOpen ? "open" : ""} ${isCollapsed ? "collapsed" : ""}`}>
        {/* Header */}
        <div className="drawer-header">
          <h2 className="drawer-title">{showArchived ? "Archived" : "Conversations"}</h2>
          <div className="drawer-header-actions">
            {/* New conversation button - mobile only */}
            {!showArchived && (
              <button
                onClick={onNewConversation}
                className="btn-icon hide-on-desktop"
                aria-label="New conversation"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M12 4v16m8-8H4"
                  />
                </svg>
              </button>
            )}
            <button
              onClick={onClose}
              className="btn-icon hide-on-desktop"
              aria-label="Close conversations"
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M6 18L18 6M6 6l12 12"
                />
              </svg>
            </button>
            {/* Collapse button - desktop only */}
            <button
              onClick={onToggleCollapse}
              className="btn-icon show-on-desktop-only"
              aria-label="Collapse sidebar"
              title="Collapse sidebar"
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M11 19l-7-7 7-7m8 14l-7-7 7-7"
                />
              </svg>
            </button>
          </div>
        </div>

        {/* Conversations list */}
        <div className="drawer-body scrollable">
          {loadingArchived && showArchived ? (
            <div style={{ padding: "1rem", textAlign: "center" }} className="text-secondary">
              <p>Loading...</p>
            </div>
          ) : displayedConversations.length === 0 ? (
            <div style={{ padding: "1rem", textAlign: "center" }} className="text-secondary">
              <p>{showArchived ? "No archived conversations" : "No conversations yet"}</p>
              {!showArchived && (
                <p className="text-sm" style={{ marginTop: "0.25rem" }}>
                  Start a new conversation to get started
                </p>
              )}
            </div>
          ) : (
            <div className="conversation-list">
              {displayedConversations.map((conversation) => {
                const isActive = conversation.conversation_id === currentConversationId;
                const hasSubagents = subagents[conversation.conversation_id]?.length > 0;
                const isExpanded = expandedSubagents.has(conversation.conversation_id);
                const conversationSubagents = subagents[conversation.conversation_id] || [];
                return (
                  <React.Fragment key={conversation.conversation_id}>
                    <div
                      className={`conversation-item ${isActive ? "active" : ""}`}
                      onClick={() => {
                        if (!showArchived) {
                          onSelectConversation(conversation);
                        }
                      }}
                      style={{ cursor: showArchived ? "default" : "pointer" }}
                    >
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
                          <div style={{ flex: 1, minWidth: 0 }}>
                            {editingId === conversation.conversation_id ? (
                              <input
                                ref={renameInputRef}
                                type="text"
                                value={editingSlug}
                                onChange={(e) => setEditingSlug(e.target.value)}
                                onBlur={() => handleRename(conversation.conversation_id)}
                                onKeyDown={(e) =>
                                  handleRenameKeyDown(e, conversation.conversation_id)
                                }
                                onClick={(e) => e.stopPropagation()}
                                autoFocus
                                className="conversation-title"
                                style={{
                                  width: "100%",
                                  background: "transparent",
                                  border: "none",
                                  borderBottom: "1px solid var(--text-secondary)",
                                  outline: "none",
                                  padding: 0,
                                  font: "inherit",
                                  color: "inherit",
                                }}
                              />
                            ) : (
                              <div className="conversation-title">
                                {getConversationPreview(conversation)}
                              </div>
                            )}
                          </div>
                          {(conversation as ConversationWithState).working && (
                            <span
                              className="working-indicator"
                              title="Agent is working"
                              style={{
                                width: "8px",
                                height: "8px",
                                borderRadius: "50%",
                                backgroundColor: "var(--accent-color, #3b82f6)",
                                flexShrink: 0,
                                animation: "pulse 2s ease-in-out infinite",
                              }}
                            />
                          )}
                        </div>
                        {(parseTags(conversation).length > 0 ||
                          tagEditingId === conversation.conversation_id) && (
                          <div className="conversation-tags">
                            {parseTags(conversation).map((tag) => (
                              <span key={tag} className="conversation-tag">
                                {tag}
                                {!showArchived && (
                                  <button
                                    className="conversation-tag-remove"
                                    onClick={(e) => handleRemoveTag(e, conversation, tag)}
                                    title={`Remove tag "${tag}"`}
                                    aria-label={`Remove tag ${tag}`}
                                  >
                                    ×
                                  </button>
                                )}
                              </span>
                            ))}
                            {tagEditingId === conversation.conversation_id && (
                              <input
                                ref={tagInputRef}
                                type="text"
                                value={tagInput}
                                placeholder="add tag…"
                                className="conversation-tag-input"
                                onChange={(e) => setTagInput(e.target.value)}
                                onKeyDown={(e) => handleTagKeyDown(e, conversation)}
                                onBlur={() => {
                                  setTagEditingId(null);
                                  setTagInput("");
                                }}
                                onClick={(e) => e.stopPropagation()}
                              />
                            )}
                          </div>
                        )}
                        <div className="conversation-meta">
                          <span className="conversation-date">
                            {formatDate(conversation.updated_at)}
                          </span>
                          {conversation.cwd && (
                            <span className="conversation-cwd" title={conversation.cwd}>
                              {formatCwdForDisplay(conversation.cwd)}
                            </span>
                          )}
                          {!showArchived && (conversation as ConversationWithState).pr && (
                            <PRBadge
                              pr={(conversation as ConversationWithState).pr!}
                              onClick={() => onOpenPRPanel?.(conversation.conversation_id)}
                            />
                          )}
                          {!showArchived && (
                            <div
                              className="conversation-actions"
                              style={{ display: "flex", gap: "0.25rem", marginLeft: "auto" }}
                            >
                              <button
                                onClick={(e) => handleStartRename(e, conversation)}
                                className="btn-icon-sm"
                                title="Rename"
                                aria-label="Rename conversation"
                              >
                                <svg
                                  fill="none"
                                  stroke="currentColor"
                                  viewBox="0 0 24 24"
                                  style={{ width: "1rem", height: "1rem" }}
                                >
                                  <path
                                    strokeLinecap="round"
                                    strokeLinejoin="round"
                                    strokeWidth={2}
                                    d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                                  />
                                </svg>
                              </button>
                              <button
                                onClick={(e) => handleStartTagEdit(e, conversation)}
                                className="btn-icon-sm"
                                title="Tags"
                                aria-label="Edit conversation tags"
                              >
                                <svg
                                  fill="none"
                                  stroke="currentColor"
                                  viewBox="0 0 24 24"
                                  style={{ width: "1rem", height: "1rem" }}
                                >
                                  <path
                                    strokeLinecap="round"
                                    strokeLinejoin="round"
                                    strokeWidth={2}
                                    d="M7 7h.01M7 3h5a1.99 1.99 0 011.414.586l7 7a2 2 0 010 2.828l-7 7a2 2 0 01-2.828 0l-7-7A1.99 1.99 0 013 12V7a4 4 0 014-4z"
                                  />
                                </svg>
                              </button>
                              <button
                                onClick={(e) => handleArchive(e, conversation.conversation_id)}
                                className="btn-icon-sm"
                                title="Archive"
                                aria-label="Archive conversation"
                              >
                                <svg
                                  fill="none"
                                  stroke="currentColor"
                                  viewBox="0 0 24 24"
                                  style={{ width: "1rem", height: "1rem" }}
                                >
                                  <path
                                    strokeLinecap="round"
                                    strokeLinejoin="round"
                                    strokeWidth={2}
                                    d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
                                  />
                                </svg>
                              </button>
                              {/* Subagent count indicator */}
                              {hasSubagents && (
                                <button
                                  onClick={(e) => toggleSubagents(e, conversation.conversation_id)}
                                  className="btn-icon-sm"
                                  style={{
                                    display: "flex",
                                    alignItems: "center",
                                    gap: "0.125rem",
                                    fontSize: "0.75rem",
                                    minWidth: "auto",
                                    padding: "0.125rem 0.25rem",
                                  }}
                                  title={isExpanded ? "Hide subagents" : "Show subagents"}
                                  aria-label={
                                    isExpanded ? "Collapse subagents" : "Expand subagents"
                                  }
                                >
                                  <span style={{ fontWeight: 500 }}>
                                    {conversationSubagents.length}
                                  </span>
                                  <svg
                                    fill="none"
                                    stroke="currentColor"
                                    viewBox="0 0 24 24"
                                    style={{
                                      width: "0.625rem",
                                      height: "0.625rem",
                                      transform: isExpanded ? "rotate(90deg)" : "rotate(0deg)",
                                      transition: "transform 0.15s ease",
                                    }}
                                  >
                                    <path
                                      strokeLinecap="round"
                                      strokeLinejoin="round"
                                      strokeWidth={2}
                                      d="M9 5l7 7-7 7"
                                    />
                                  </svg>
                                </button>
                              )}
                            </div>
                          )}
                        </div>
                      </div>
                      {showArchived && (
                        <div
                          className="conversation-actions"
                          style={{ display: "flex", gap: "0.25rem", marginLeft: "0.5rem" }}
                        >
                          <button
                            onClick={(e) => handleUnarchive(e, conversation.conversation_id)}
                            className="btn-icon-sm"
                            title="Restore"
                            aria-label="Restore conversation"
                          >
                            <svg
                              fill="none"
                              stroke="currentColor"
                              viewBox="0 0 24 24"
                              style={{ width: "1rem", height: "1rem" }}
                            >
                              <path
                                strokeLinecap="round"
                                strokeLinejoin="round"
                                strokeWidth={2}
                                d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                              />
                            </svg>
                          </button>
                          <button
                            onClick={(e) => handleDelete(e, conversation.conversation_id)}
                            className="btn-icon-sm btn-danger"
                            title="Delete permanently"
                            aria-label="Delete conversation"
                          >
                            <svg
                              fill="none"
                              stroke="currentColor"
                              viewBox="0 0 24 24"
                              style={{ width: "1rem", height: "1rem" }}
                            >
                              <path
                                strokeLinecap="round"
                                strokeLinejoin="round"
                                strokeWidth={2}
                                d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                              />
                            </svg>
                          </button>
                        </div>
                      )}
                    </div>
                    {/* Render subagents if expanded */}
                    {!showArchived && isExpanded && conversationSubagents.length > 0 && (
                      <div className="subagent-list" style={{ marginLeft: "1.5rem" }}>
                        {conversationSubagents.map((sub) => {
                          const isSubActive = sub.conversation_id === currentConversationId;
                          return (
                            <div
                              key={sub.conversation_id}
                              className={`conversation-item subagent-item ${isSubActive ? "active" : ""}`}
                              onClick={() => onSelectConversation(sub)}
                              style={{
                                cursor: "pointer",
                                fontSize: "0.9em",
                                paddingLeft: "0.5rem",
                                borderLeft: "2px solid var(--border-color)",
                              }}
                            >
                              <div style={{ flex: 1, minWidth: 0 }}>
                                <div
                                  style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}
                                >
                                  <div style={{ flex: 1, minWidth: 0 }}>
                                    <div className="conversation-title">
                                      {sub.slug || sub.conversation_id}
                                    </div>
                                  </div>
                                  {sub.working && (
                                    <span
                                      className="working-indicator"
                                      title="Subagent is working"
                                      style={{
                                        width: "6px",
                                        height: "6px",
                                        borderRadius: "50%",
                                        backgroundColor: "var(--accent-color, #3b82f6)",
                                        flexShrink: 0,
                                        animation: "pulse 2s ease-in-out infinite",
                                      }}
                                    />
                                  )}
                                </div>
                                <div className="conversation-meta">
                                  <span
                                    className="conversation-date"
                                    style={{ fontSize: "0.85em" }}
                                  >
                                    {formatDate(sub.updated_at)}
                                  </span>
                                </div>
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    )}
                  </React.Fragment>
                );
              })}
            </div>
          )}
        </div>

        {/* Footer with archived toggle and cost dashboard */}
        <div className="drawer-footer" style={{ display: "flex", gap: "0.5rem" }}>
          <button
            onClick={() => setShowArchived(!showArchived)}
            className="btn-secondary"
            style={{
              flex: 1,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              gap: "0.5rem",
            }}
          >
            <svg
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
              style={{ width: "1rem", height: "1rem" }}
            >
              {showArchived ? (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M11 15l-3-3m0 0l3-3m-3 3h8M3 12a9 9 0 1118 0 9 9 0 01-18 0z"
                />
              ) : (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
                />
              )}
            </svg>
            <span>{showArchived ? "Back to Conversations" : "View Archived"}</span>
          </button>
          <button
            onClick={() => setShowSkillsModal(true)}
            className="btn-secondary"
            title="Skills"
            style={{
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              padding: "0.5rem",
            }}
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="20" height="20">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M13 10V3L4 14h7v7l9-11h-7z"
              />
            </svg>
          </button>
          {onShowCostDashboard && (
            <button
              onClick={onShowCostDashboard}
              className="btn-secondary"
              title="Usage & Costs"
              style={{
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                padding: "0.5rem",
              }}
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="20" height="20">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z"
                />
              </svg>
            </button>
          )}
        </div>
      </div>
      {showSkillsModal && (
        <Modal
          isOpen
          onClose={() => setShowSkillsModal(false)}
          title="Skills"
          className="skills-modal"
        >
          <SkillsList cwd={viewedConversation?.cwd || undefined} />
        </Modal>
      )}
    </>
  );
}

export default ConversationDrawer;

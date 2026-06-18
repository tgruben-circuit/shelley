import {
  Conversation,
  ConversationWithState,
  StreamResponse,
  ChatRequest,
  GitDiffInfo,
  GitFileInfo,
  GitFileDiff,
  VersionInfo,
  CommitInfo,
  PRResponse,
  SearchHit,
} from "../types";

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

class ApiService {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  async getConversations(): Promise<ConversationWithState[]> {
    const response = await fetch(`${this.baseUrl}/conversations`);
    if (!response.ok) {
      throw new Error(`Failed to get conversations: ${response.statusText}`);
    }
    return response.json();
  }

  async getModels(): Promise<
    Array<{
      id: string;
      display_name?: string;
      source?: string;
      ready: boolean;
      max_context_tokens?: number;
    }>
  > {
    const response = await fetch(`${this.baseUrl}/models`);
    if (!response.ok) {
      throw new Error(`Failed to get models: ${response.statusText}`);
    }
    return response.json();
  }

  async searchMessages(query: string): Promise<SearchHit[]> {
    const params = new URLSearchParams({ q: query });
    const response = await fetch(`${this.baseUrl}/conversations/search?${params}`);
    if (!response.ok) {
      throw new Error(`Failed to search messages: ${response.statusText}`);
    }
    return response.json();
  }

  async sendMessageWithNewConversation(request: ChatRequest): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/new`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to send message: ${response.statusText}`);
    }
    return response.json();
  }

  async continueConversation(
    sourceConversationId: string,
    model?: string,
    cwd?: string,
  ): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/continue`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({
        source_conversation_id: sourceConversationId,
        model: model || "",
        cwd: cwd || "",
      }),
    });
    if (!response.ok) {
      throw new Error(`Failed to continue conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async distillConversation(
    sourceConversationId: string,
    model?: string,
    cwd?: string,
  ): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/distill`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({
        source_conversation_id: sourceConversationId,
        model: model || "",
        cwd: cwd || "",
      }),
    });
    if (!response.ok) {
      throw new Error(`Failed to distill conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async getConversation(conversationId: string): Promise<StreamResponse> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}`);
    if (!response.ok) {
      throw new Error(`Failed to get messages: ${response.statusText}`);
    }
    return response.json();
  }

  async sendMessage(conversationId: string, request: ChatRequest): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/chat`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to send message: ${response.statusText}`);
    }
  }

  async critiqueConversation(
    conversationId: string,
    model?: string,
  ): Promise<{ status: string; message_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/critique`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(model ? { model } : {}),
    });
    if (!response.ok) {
      const text = (await response.text()).trim();
      throw new ApiError(text || `Failed to critique: ${response.statusText}`, response.status);
    }
    return response.json();
  }

  async switchConversationModel(
    conversationId: string,
    model: string,
    cancelCurrentTurn = false,
  ): Promise<{ status: string; model: string }> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/switch-model`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ model, cancel_current_turn: cancelCurrentTurn }),
    });
    if (!response.ok) {
      const text = (await response.text()).trim();
      throw new ApiError(text || `Failed to switch model: ${response.statusText}`, response.status);
    }
    return response.json();
  }

  createMessageStream(conversationId: string, lastSequenceId?: number): EventSource {
    let url = `${this.baseUrl}/conversation/${conversationId}/stream`;
    if (lastSequenceId !== undefined && lastSequenceId >= 0) {
      url += `?last_sequence_id=${lastSequenceId}`;
    }
    return new EventSource(url);
  }

  async cancelConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/cancel`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to cancel conversation: ${response.statusText}`);
    }
  }

  async validateCwd(path: string): Promise<{ valid: boolean; error?: string }> {
    const response = await fetch(`${this.baseUrl}/validate-cwd?path=${encodeURIComponent(path)}`);
    if (!response.ok) {
      throw new Error(`Failed to validate cwd: ${response.statusText}`);
    }
    return response.json();
  }

  async listDirectory(path?: string): Promise<{
    path: string;
    parent: string;
    entries: Array<{ name: string; is_dir: boolean; git_head_subject?: string }>;
    git_head_subject?: string;
    git_worktree_root?: string;
    error?: string;
  }> {
    const url = path
      ? `${this.baseUrl}/list-directory?path=${encodeURIComponent(path)}`
      : `${this.baseUrl}/list-directory`;
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`Failed to list directory: ${response.statusText}`);
    }
    return response.json();
  }

  async createDirectory(path: string): Promise<{ path?: string; error?: string }> {
    const response = await fetch(`${this.baseUrl}/create-directory`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ path }),
    });
    if (!response.ok) {
      throw new Error(`Failed to create directory: ${response.statusText}`);
    }
    return response.json();
  }

  async getArchivedConversations(): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversations/archived`);
    if (!response.ok) {
      throw new Error(`Failed to get archived conversations: ${response.statusText}`);
    }
    return response.json();
  }

  async archiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/archive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to archive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async unarchiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/unarchive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to unarchive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/delete`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to delete conversation: ${response.statusText}`);
    }
  }

  async getConversationBySlug(slug: string): Promise<Conversation | null> {
    const response = await fetch(
      `${this.baseUrl}/conversation-by-slug/${encodeURIComponent(slug)}`,
    );
    if (response.status === 404) {
      return null;
    }
    if (!response.ok) {
      throw new Error(`Failed to get conversation by slug: ${response.statusText}`);
    }
    return response.json();
  }

  // Git diff APIs
  async getGitDiffs(cwd: string): Promise<{ diffs: GitDiffInfo[]; gitRoot: string }> {
    const response = await fetch(`${this.baseUrl}/git/diffs?cwd=${encodeURIComponent(cwd)}`);
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async getGitDiffFiles(diffId: string, cwd: string): Promise<GitFileInfo[]> {
    const response = await fetch(
      `${this.baseUrl}/git/diffs/${diffId}/files?cwd=${encodeURIComponent(cwd)}`,
    );
    if (!response.ok) {
      throw new Error(`Failed to get diff files: ${response.statusText}`);
    }
    return response.json();
  }

  async getGitFileDiff(diffId: string, filePath: string, cwd: string): Promise<GitFileDiff> {
    const response = await fetch(
      `${this.baseUrl}/git/file-diff/${diffId}/${filePath}?cwd=${encodeURIComponent(cwd)}`,
    );
    if (!response.ok) {
      throw new Error(`Failed to get file diff: ${response.statusText}`);
    }
    return response.json();
  }

  async renameConversation(conversationId: string, slug: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/rename`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ slug }),
    });
    if (!response.ok) {
      throw new Error(`Failed to rename conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async getPR(conversationId: string): Promise<PRResponse> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/pr`);
    if (!response.ok) {
      throw new Error(`Failed to get PR: ${response.statusText}`);
    }
    return response.json();
  }

  private async postPR(conversationId: string, action: string, body: unknown): Promise<PRResponse> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/pr/${action}`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      const text = (await response.text()).trim();
      throw new Error(text || `Failed to ${action}: ${response.statusText}`);
    }
    return response.json();
  }

  async replyToPRThread(
    conversationId: string,
    threadId: string,
    body: string,
  ): Promise<PRResponse> {
    return this.postPR(conversationId, "reply", { thread_id: threadId, body });
  }

  async resolvePRThread(
    conversationId: string,
    threadId: string,
    resolved: boolean,
  ): Promise<PRResponse> {
    return this.postPR(conversationId, "resolve", { thread_id: threadId, resolved });
  }

  async commentOnPR(conversationId: string, body: string): Promise<PRResponse> {
    return this.postPR(conversationId, "comment", { body });
  }

  async getSubagents(conversationId: string): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/subagents`);
    if (!response.ok) {
      throw new Error(`Failed to get subagents: ${response.statusText}`);
    }
    return response.json();
  }

  async getSkills(cwd?: string): Promise<SkillSummary[]> {
    const url = cwd
      ? `${this.baseUrl}/skills?cwd=${encodeURIComponent(cwd)}`
      : `${this.baseUrl}/skills`;
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`Failed to get skills: ${response.statusText}`);
    }
    return response.json();
  }

  async getSkillContent(name: string, cwd?: string): Promise<SkillContent> {
    const url = cwd
      ? `${this.baseUrl}/skills/${encodeURIComponent(name)}?cwd=${encodeURIComponent(cwd)}`
      : `${this.baseUrl}/skills/${encodeURIComponent(name)}`;
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`Failed to load skill content: ${response.statusText}`);
    }
    return response.json();
  }

  // Version check APIs
  async checkVersion(forceRefresh = false): Promise<VersionInfo> {
    const url = forceRefresh ? "/version-check?refresh=true" : "/version-check";
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`Failed to check version: ${response.statusText}`);
    }
    return response.json();
  }

  async getChangelog(currentTag: string, latestTag: string): Promise<CommitInfo[]> {
    const params = new URLSearchParams({ current: currentTag, latest: latestTag });
    const response = await fetch(`/version-changelog?${params}`);
    if (!response.ok) {
      throw new Error(`Failed to get changelog: ${response.statusText}`);
    }
    return response.json();
  }

  async upgrade(restart: boolean = false): Promise<{ status: string; message: string }> {
    const url = restart ? "/upgrade?restart=true" : "/upgrade";
    const response = await fetch(url, {
      method: "POST",
      headers: { "X-Percy-Request": "1" },
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async exit(): Promise<{ status: string; message: string }> {
    const response = await fetch("/exit", {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to exit: ${response.statusText}`);
    }
    return response.json();
  }

  async getSettings(): Promise<Record<string, string>> {
    const response = await fetch("/settings");
    if (!response.ok) {
      throw new Error(`Failed to get settings: ${response.statusText}`);
    }
    return response.json();
  }

  async setSetting(key: string, value: string): Promise<{ status: string }> {
    const response = await fetch("/settings", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Percy-Request": "1",
      },
      body: JSON.stringify({ key, value }),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }
  async getUsage(since: string): Promise<{
    by_date: Array<{
      date: string;
      model: string | null;
      message_count: number;
      total_input_tokens: number;
      total_output_tokens: number;
      total_cost_usd: number;
    }>;
    by_conversation: Array<{
      conversation_id: string;
      slug: string | null;
      model: string | null;
      message_count: number;
      total_input_tokens: number;
      total_output_tokens: number;
      total_cost_usd: number;
    }>;
    total_cost_usd: number;
  }> {
    const response = await fetch(`${this.baseUrl}/usage?since=${since}`);
    if (!response.ok) {
      throw new Error(`Failed to get usage: ${response.statusText}`);
    }
    return response.json();
  }
  async forkConversation(
    sourceConversationId: string,
    atSequenceId: number,
    model?: string,
  ): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/fork`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({
        source_conversation_id: sourceConversationId,
        at_sequence_id: atSequenceId,
        model,
      }),
    });
    if (!response.ok) throw new Error(`Failed to fork: ${response.statusText}`);
    return response.json();
  }

  async getTouchedFiles(
    conversationId: string,
  ): Promise<Array<{ path: string; operation: string; count: number }>> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/files`);
    if (!response.ok) throw new Error(`Failed to get files: ${response.statusText}`);
    return response.json();
  }

  async editMessage(conversationId: string, sequenceId: number, message: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/edit`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ sequence_id: sequenceId, message }),
    });
    if (!response.ok) throw new Error(`Failed to edit: ${response.statusText}`);
  }

  async regenerateMessage(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/regenerate`, {
      method: "POST",
      headers: this.postHeaders,
    });
    if (!response.ok) throw new Error(`Failed to regenerate: ${response.statusText}`);
  }
}

export const api = new ApiService();

export interface SkillSummary {
  name: string;
  description: string;
  path: string;
  scope: string; // "project", "user", or "builtin"
}

export interface SkillContent {
  name: string;
  description: string;
  path: string;
  scope: string;
  content: string;
}

// Custom models API
export interface CustomModel {
  model_id: string;
  display_name: string;
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini" | "ollama";
  endpoint: string;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string; // Comma-separated tags (e.g., "slug" for slug generation)
  thinking_level: ThinkingLevel;
}

export type ThinkingLevel = "off" | "minimal" | "low" | "medium" | "high";

export interface CreateCustomModelRequest {
  display_name: string;
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini" | "ollama";
  endpoint: string;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string; // Comma-separated tags
  thinking_level: ThinkingLevel;
}

export interface TestCustomModelRequest {
  model_id?: string; // If provided with empty api_key, use stored key
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini" | "ollama";
  endpoint: string;
  api_key: string;
  model_name: string;
}

class CustomModelsApi {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  async getCustomModels(): Promise<CustomModel[]> {
    const response = await fetch(`${this.baseUrl}/custom-models`);
    if (!response.ok) {
      throw new Error(`Failed to get custom models: ${response.statusText}`);
    }
    return response.json();
  }

  async createCustomModel(request: CreateCustomModelRequest): Promise<CustomModel> {
    const response = await fetch(`${this.baseUrl}/custom-models`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to create custom model: ${response.statusText}`);
    }
    return response.json();
  }

  async updateCustomModel(
    modelId: string,
    request: Partial<CreateCustomModelRequest>,
  ): Promise<CustomModel> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}`, {
      method: "PUT",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to update custom model: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteCustomModel(modelId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}`, {
      method: "DELETE",
    });
    if (!response.ok) {
      throw new Error(`Failed to delete custom model: ${response.statusText}`);
    }
  }

  async duplicateCustomModel(modelId: string, displayName?: string): Promise<CustomModel> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}/duplicate`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ display_name: displayName }),
    });
    if (!response.ok) {
      throw new Error(`Failed to duplicate custom model: ${response.statusText}`);
    }
    return response.json();
  }

  async testCustomModel(
    request: TestCustomModelRequest,
  ): Promise<{ success: boolean; message: string }> {
    const response = await fetch(`${this.baseUrl}/custom-models-test`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to test custom model: ${response.statusText}`);
    }
    return response.json();
  }
}

export const customModelsApi = new CustomModelsApi();

// Notification channels API
export interface NotificationChannelAPI {
  channel_id: string;
  channel_type: string;
  display_name: string;
  enabled: boolean;
  config: Record<string, string>;
}

export interface CreateNotificationChannelRequest {
  channel_type: string;
  display_name: string;
  enabled: boolean;
  config: Record<string, string>;
}

export interface UpdateNotificationChannelRequest {
  display_name: string;
  enabled: boolean;
  config: Record<string, string>;
}

export interface ChannelTypeInfo {
  type: string;
  label: string;
  config_fields: {
    name: string;
    label: string;
    type: string;
    required: boolean;
    placeholder?: string;
  }[];
}

class NotificationChannelsApi {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  async getChannels(): Promise<NotificationChannelAPI[]> {
    const response = await fetch(`${this.baseUrl}/notification-channels`);
    if (!response.ok) {
      throw new Error(`Failed to get notification channels: ${response.statusText}`);
    }
    return response.json();
  }

  async createChannel(request: CreateNotificationChannelRequest): Promise<NotificationChannelAPI> {
    const response = await fetch(`${this.baseUrl}/notification-channels`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to create notification channel: ${response.statusText}`);
    }
    return response.json();
  }

  async updateChannel(
    channelId: string,
    request: UpdateNotificationChannelRequest,
  ): Promise<NotificationChannelAPI> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}`, {
      method: "PUT",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to update notification channel: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteChannel(channelId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}`, {
      method: "DELETE",
    });
    if (!response.ok) {
      throw new Error(`Failed to delete notification channel: ${response.statusText}`);
    }
  }

  async testChannel(channelId: string): Promise<{ success: boolean; message: string }> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}/test`, {
      method: "POST",
      headers: this.postHeaders,
    });
    if (!response.ok) {
      throw new Error(`Failed to test notification channel: ${response.statusText}`);
    }
    return response.json();
  }
}

export const notificationChannelsApi = new NotificationChannelsApi();

class PushApi {
  private baseUrl = "/api/push";
  private headers = { "Content-Type": "application/json" };

  async getVapidPublicKey(): Promise<string> {
    const response = await fetch(`${this.baseUrl}/vapid-public-key`);
    if (!response.ok) throw new Error("Failed to get VAPID public key");
    const data: { public_key: string } = await response.json();
    return data.public_key;
  }

  async subscribe(subscription: PushSubscriptionJSON): Promise<string> {
    const keys = subscription.keys as { p256dh: string; auth: string } | undefined;
    const response = await fetch(`${this.baseUrl}/subscribe`, {
      method: "POST",
      headers: this.headers,
      body: JSON.stringify({
        endpoint: subscription.endpoint,
        p256dh: keys?.p256dh ?? "",
        auth: keys?.auth ?? "",
        user_agent: navigator.userAgent,
      }),
    });
    if (!response.ok) {
      const msg = await response.text().catch(() => "");
      throw new Error(msg.trim() || "Failed to register push subscription");
    }
    const data: { id: string } = await response.json();
    return data.id;
  }

  async unsubscribe(endpoint: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/unsubscribe`, {
      method: "POST",
      headers: this.headers,
      body: JSON.stringify({ endpoint }),
    });
    if (!response.ok && response.status !== 404) {
      throw new Error("Failed to unsubscribe from push notifications");
    }
  }
}

export const pushApi = new PushApi();

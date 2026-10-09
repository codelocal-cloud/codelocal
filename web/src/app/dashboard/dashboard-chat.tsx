"use client";
/* eslint-disable @next/next/no-img-element -- chat images are user-provided data/blob previews and should not be optimized remotely */

import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useRef, useState, type ChangeEvent, type ClipboardEvent, type FormEvent, type KeyboardEvent } from "react";
import { isAccountResource } from "@/lib/contracts/account";
import { isWorkspacesResource, type WorkspacesResource } from "@/lib/contracts/resources";
import { isRuntimeSettingsResource, type RuntimeExecutionMode } from "@/lib/contracts/runtime-settings";
import { useTranslations } from "@/lib/i18n/provider";
import type { MessageKey } from "@/lib/i18n/messages";
import { AppIcon } from "./app-icon";
import { ChatActionSummary, type ChatToolCall } from "./chat-action-summary";
import { ChatContextSheet } from "./chat-context-sheet";
import { ChatNewTaskDialog, GENERAL_PROJECT_KEY, type ChatNewTaskModel, type ChatNewTaskProject } from "./chat-new-task-dialog";
import { ChatProviderManager } from "./chat-provider-manager";
import { ChatTopBar } from "./chat-top-bar";
import { ChatRichMessage } from "./chat-rich-message";
import { shouldDispatchQueuedPrompt, type ChatRunOutcome, type QueuedChatPrompt } from "./chat-queue";
import { ChatSidebarBrand, ChatSidebarFooter } from "./chat-sidebar-footer";
import { chatViewportFrame } from "./chat-visual-viewport";
import { projectTreeOpen, threadBelongsToWorkspace, threadCreationPayload, toggleExpandedProject, workspaceIdentityKey } from "./chat-workspace-key";
import { useDashboardResource } from "./use-dashboard-resource";
import headerStyles from "./chat-header.module.css";
import mobileStyles from "./chat-mobile.module.css";
import styles from "./dashboard-chat.module.css";
import treeStyles from "./chat-project-tree.module.css";
import skillStyles from "./skill-indicator.module.css";

type ToolCall = ChatToolCall;

type SkillBadge = {
  id: string;
  name: string;
  version: string;
};

type ChatMsg = {
  role: "user" | "assistant";
  content: string;
  tool_calls?: ToolCall[];
  image?: string;
  skills?: SkillBadge[];
};

type ChatMode = "ask" | "plan" | "agent";
type ChatContextMode = "smart" | "off" | "aggressive";
type ChatModelOption = {
  id: string;
  label: string;
  provider: string;
  providerId?: string;
  custom?: boolean;
};

type ChatThread = {
  id: string;
  title: string;
  model: string;
  workspaceKey?: string;
  createdAt: number;
  updatedAt: number;
};

type StreamData = {
  delta?: string;
  content?: string;
  reply?: string;
  error?: string;
  threadId?: string;
  tool_calls?: ToolCall[] | Array<{ index: number; name?: string; arguments?: string; id?: string }>;
};

type WorkspaceItem = WorkspacesResource["items"][number];

type ChatImageMeta = {
  imageRef: string;
  sha256: string;
  contentType: string;
  size: number;
};

type PreparedChatImage = {
  previewUrl: string;
  file: File;
};

type ChatNotice = { kind: "status" | "error"; text: string } | null;

type MediaPrepareResponse = ChatImageMeta & {
  url: string;
  upload: {
    required: boolean;
    url?: string;
    method?: string;
    headers?: Record<string, string[]>;
  };
};

const suggestions: MessageKey[] = ["Explain this codebase", "Find and fix a bug", "Add tests for recent changes"];

function modelLabel(model: string) {
  return model === "auto" ? "Auto" : model;
}

function workspaceKey(workspace: WorkspaceItem) {
  return workspaceIdentityKey(workspace);
}

function runtimeSettingsURL(workspace: WorkspaceItem) {
  const params = new URLSearchParams({ scope: "workspace", deviceId: workspace.deviceId, workspaceId: workspace.workspaceId });
  return `/api/v1/runtime/settings?${params.toString()}`;
}

function workspaceStatusLabel(workspace: WorkspaceItem): MessageKey {
  if (!workspace.runtimeOnline || workspace.status === "offline") return "Offline";
  if (workspace.status === "sleeping") return "Sleeping";
  return "Online";
}

function workspaceState(workspace: WorkspaceItem) {
  if (!workspace.runtimeOnline || workspace.status === "offline") return "offline";
  return workspace.status;
}


function parseSkillBadges(value: unknown): SkillBadge[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!item || typeof item !== "object") return [];
    const record = item as Record<string, unknown>;
    const id = typeof record.id === "string" ? record.id.trim() : "";
    const name = typeof record.name === "string" ? record.name.trim() : "";
    const version = typeof record.version === "string" ? record.version.trim() : "";
    return id && name && version ? [{ id, name, version }] : [];
  }).slice(0, 3);
}

function parseSkillHeader(raw: string | null): SkillBadge[] {
  if (!raw) return [];
  try {
    return parseSkillBadges(JSON.parse(raw) as unknown);
  } catch {
    return [];
  }
}

function parseHistorySkills(raw: string | SkillBadge[] | undefined): SkillBadge[] {
  if (Array.isArray(raw)) return parseSkillBadges(raw);
  return parseSkillHeader(raw ?? null);
}

function historyMessages(value: unknown): ChatMsg[] {
  if (!value || typeof value !== "object") return [];
  const records = (value as { messages?: unknown }).messages;
  if (!Array.isArray(records)) return [];
  return records.flatMap((entry) => {
    if (!entry || typeof entry !== "object") return [];
    const message = entry as { role?: unknown; content?: unknown; tool_calls?: string | ToolCall[]; skills?: string | SkillBadge[]; image?: unknown };
    if ((message.role !== "user" && message.role !== "assistant") || typeof message.content !== "string") return [];
    let toolCalls: ToolCall[] | undefined;
    if (typeof message.tool_calls === "string") {
      try {
        const parsed = JSON.parse(message.tool_calls) as unknown;
        if (Array.isArray(parsed)) toolCalls = parsed as ToolCall[];
      } catch {
        toolCalls = undefined;
      }
    } else if (Array.isArray(message.tool_calls)) {
      toolCalls = message.tool_calls;
    }
    const skills = parseHistorySkills(message.skills);
    return [{
      role: message.role,
      content: message.content,
      tool_calls: toolCalls,
      skills: skills.length ? skills : undefined,
      image: typeof message.image === "string" ? message.image : undefined,
    } satisfies ChatMsg];
  }).slice(-50);
}

const chatTransportRetryDelays = [350, 900, 1800];

function createChatRequestId() {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  return `chat-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

function isRetryableChatTransportError(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  return /load failed|failed to fetch|fetch failed|network request failed|network error|body stream|connection.*lost|terminated|chat_stream_incomplete|HTTP (408|425|429|500|502|503|504)/i.test(message);
}

function waitForChatRetry(delayMs: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const timer = window.setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, delayMs);
    const onAbort = () => {
      window.clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function friendlyChatFailure(raw: string, t: ReturnType<typeof useTranslations>["t"], translateMessage: ReturnType<typeof useTranslations>["message"]) {
  if (/vision-capable|does not support images|model vision|chưa có model vision/i.test(raw)) {
    return t("CodeLocal does not have a vision model available for this image. Configure CODELOCAL_SHOPAIKEY_API_KEY with a vision-capable model such as gpt-* and try again.");
  }
  if (/chưa bật upload ảnh|media_upload_incomplete|media_not_configured/i.test(raw)) {
    return t("Image upload storage (S3) is not configured. The image can still be sent directly but will only be stored compactly in chat history. Retry the image if the backend reports media_upload_incomplete.");
  }
  if (/Model community miễn phí|không nhận.*ảnh|workspace/i.test(raw)) return raw;
  if (/508|tool loop|loop exceeded/i.test(raw)) {
    return t("That processing run became too long. CodeLocal kept the completed work; send “continue” to resume.");
  }
  const rateLimit = /RATE_LIMIT:(\d+)/.exec(raw);
  if (rateLimit) return t("You are sending too quickly. Try again in {seconds}s.", { seconds: rateLimit[1] });
  if (/load failed|chat_stream_incomplete|timeout|502|503|504|network|fetch/i.test(raw)) {
    return t("The connection was interrupted after several automatic retries. Completed work was preserved; send “continue” to resume.");
  }
  const known = translateMessage(raw);
  return known !== raw ? known : t("An error occurred while processing the request: {message}", { message: raw });
}
export function DashboardChat() {
  const { locale, t, message: translateMessage } = useTranslations();
  const router = useRouter();
  const searchParams = useSearchParams();
  const [messages, setMessages] = useState<ChatMsg[]>([]);
  const [input, setInput] = useState("");
  const [loading, setLoading] = useState(false);
  const [queuedPrompt, setQueuedPrompt] = useState<QueuedChatPrompt | null>(null);
  const [runOutcome, setRunOutcome] = useState<ChatRunOutcome>("idle");
  const [mode, setMode] = useState<ChatMode>("agent");
  const [contextMode, setContextMode] = useState<ChatContextMode>("smart");
  const [goal, setGoal] = useState("");
  const [image, setImage] = useState<PreparedChatImage | null>(null);
  const [imageUploading, setImageUploading] = useState(false);
  const [notice, setNotice] = useState<ChatNotice>(null);
  const [threads, setThreads] = useState<ChatThread[]>([]);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [threadSearch, setThreadSearch] = useState("");
  const [expandedProjects, setExpandedProjects] = useState<Set<string>>(() => {
    const deviceId = searchParams.get("deviceId");
    const workspaceId = searchParams.get("workspaceId");
    return deviceId && workspaceId ? new Set([`${deviceId}::${workspaceId}`]) : new Set();
  });
  const [threadActionLoading, setThreadActionLoading] = useState(true);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [models, setModels] = useState<string[]>([]);
  const [modelOptions, setModelOptions] = useState<ChatModelOption[]>([]);
  const [selectedModel, setSelectedModel] = useState("");
  const [modelPickerOpen, setModelPickerOpen] = useState(false);
  const [providerManagerOpen, setProviderManagerOpen] = useState(false);
  const [newTaskOpen, setNewTaskOpen] = useState(false);
  const [newTaskWorkspaceKey, setNewTaskWorkspaceKey] = useState("");
  const [newTaskModel, setNewTaskModel] = useState("");
  const [newTaskExecutionMode, setNewTaskExecutionMode] = useState<RuntimeExecutionMode>("safe");
  const [newTaskExecutionConfigured, setNewTaskExecutionConfigured] = useState(false);
  const [newTaskExecutionLoading, setNewTaskExecutionLoading] = useState(false);
  const [newTaskExecutionError, setNewTaskExecutionError] = useState(false);
  const [activeExecutionMode, setActiveExecutionMode] = useState<RuntimeExecutionMode>("safe");
  const [modelSearch, setModelSearch] = useState("");
  const [contextSheetOpen, setContextSheetOpen] = useState(false);
  const [threadDrawerOpen, setThreadDrawerOpen] = useState(false);
  const [showJumpLatest, setShowJumpLatest] = useState(false);
  const [selectedWorkspaceKey, setSelectedWorkspaceKey] = useState(() => {
    const deviceId = searchParams.get("deviceId");
    const workspaceId = searchParams.get("workspaceId");
    return deviceId && workspaceId ? `${deviceId}::${workspaceId}` : "auto";
  });
  const account = useDashboardResource("/api/v1/account", isAccountResource);
  const workspaces = useDashboardResource("/api/v1/workspaces", isWorkspacesResource);
  const chatFrameRef = useRef<HTMLElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const messagesRef = useRef<HTMLDivElement>(null);
  const endRef = useRef<HTMLDivElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const contextTriggerRef = useRef<HTMLButtonElement>(null);
  const followLatestRef = useRef(true);
  const quickMessageRef = useRef<string | null>(null);
  const streamAbortRef = useRef<AbortController | null>(null);
  const modelPickerRef = useRef<HTMLDivElement>(null);
  const mobileDrawerTriggerRef = useRef<HTMLButtonElement>(null);
  const threadDrawerTriggerRef = useRef<HTMLButtonElement>(null);
  const threadDrawerCloseRef = useRef<HTMLButtonElement>(null);
  const newTaskExecutionRequestRef = useRef(0);

  useEffect(() => {
    const stored = window.localStorage.getItem("codelocal.chat.contextMode");
    if (stored !== "smart" && stored !== "off" && stored !== "aggressive") return;
    queueMicrotask(() => setContextMode(stored));
  }, []);

  useEffect(() => {
    window.localStorage.setItem("codelocal.chat.contextMode", contextMode);
  }, [contextMode]);

  useEffect(() => {
    const frame = chatFrameRef.current;
    if (!frame) return;
    const viewport = window.visualViewport;
    let animationFrame = 0;
    const updateViewport = () => {
      window.cancelAnimationFrame(animationFrame);
      animationFrame = window.requestAnimationFrame(() => {
        const next = chatViewportFrame(viewport, window.innerHeight);
        frame.style.setProperty("--chat-viewport-height", `${next.height}px`);
        frame.style.setProperty("--chat-viewport-offset-top", `${next.offsetTop}px`);
      });
    };
    updateViewport();
    viewport?.addEventListener("resize", updateViewport);
    viewport?.addEventListener("scroll", updateViewport);
    window.addEventListener("orientationchange", updateViewport);
    return () => {
      window.cancelAnimationFrame(animationFrame);
      viewport?.removeEventListener("resize", updateViewport);
      viewport?.removeEventListener("scroll", updateViewport);
      window.removeEventListener("orientationchange", updateViewport);
      frame.style.removeProperty("--chat-viewport-height");
      frame.style.removeProperty("--chat-viewport-offset-top");
    };
  }, []);

  useEffect(() => {
    if (!threadDrawerOpen) return;
    const previousOverflow = document.body.style.overflow;
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setThreadDrawerOpen(false);
      mobileDrawerTriggerRef.current?.focus();
    };
    document.documentElement.dataset.chatMenuOpen = "true";
    document.body.style.overflow = "hidden";
    document.addEventListener("keydown", closeOnEscape);
    window.requestAnimationFrame(() => threadDrawerCloseRef.current?.focus());
    return () => {
      delete document.documentElement.dataset.chatMenuOpen;
      document.body.style.overflow = previousOverflow;
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [threadDrawerOpen]);

  useEffect(() => {
    const textarea = composerRef.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${Math.min(textarea.scrollHeight, 144)}px`;
  }, [input]);

  const workspaceItems = useMemo(
    () => workspaces.state.kind === "ready" ? workspaces.state.value.items : [],
    [workspaces.state],
  );
  const selectedWorkspace = useMemo(
    () => workspaceItems.find((workspace) => workspaceKey(workspace) === selectedWorkspaceKey),
    [selectedWorkspaceKey, workspaceItems],
  );
  const csrf = account.state.kind === "ready" ? account.state.value.csrf : "";
  const activeModels = useMemo(
    () => models.filter((model) => model !== "auto"),
    [models],
  );
  const modelOptionMap = useMemo(
    () => new Map(modelOptions.map((option) => [option.id, option])),
    [modelOptions],
  );
  const newTaskProjects = useMemo<ChatNewTaskProject[]>(() => workspaceItems.map((workspace) => ({
    key: workspaceKey(workspace),
    name: workspace.workspaceName,
    deviceName: workspace.deviceName,
    statusLabel: t(workspaceStatusLabel(workspace)),
    active: Boolean(workspace.runtimeOnline && workspace.status === "active"),
  })), [t, workspaceItems]);
  const newTaskModels = useMemo<ChatNewTaskModel[]>(() => activeModels.map((model) => ({
    id: model,
    label: modelOptionMap.get(model)?.label || modelLabel(model),
    provider: modelOptionMap.get(model)?.provider || "Your AI",
  })), [activeModels, modelOptionMap]);
  const labelForModel = (model: string) => modelOptionMap.get(model)?.label || modelLabel(model);
  const providerForModel = (model: string) => modelOptionMap.get(model)?.provider || "Your AI";
  const visibleModels = useMemo(() => {
    const query = modelSearch.trim().toLocaleLowerCase(locale);
    if (!query) return activeModels;
    return activeModels.filter((model) => {
      const option = modelOptionMap.get(model);
      return `${model} ${option?.label || modelLabel(model)} ${option?.provider || "CodeLocal"}`.toLocaleLowerCase(locale).includes(query);
    });
  }, [locale, modelSearch, activeModels, modelOptionMap]);
  const activeThread = threads.find((thread) => thread.id === activeThreadId);
  const activeThreadModel = activeThread?.model;
  const activeThreadWorkspaceKey = activeThread?.workspaceKey;
  const threadTree = useMemo(() => {
    const query = threadSearch.trim().toLocaleLowerCase(locale);
    const matches = (values: Array<string | undefined>) => !query || values.some((value) => value?.toLocaleLowerCase(locale).includes(query));
    const projects = workspaceItems.flatMap((workspace) => {
      const projectThreads = threads.filter((thread) => threadBelongsToWorkspace(thread, workspace));
      const projectMatches = matches([workspace.workspaceName, workspace.deviceName, t(workspaceStatusLabel(workspace))]);
      const visibleThreads = projectMatches ? projectThreads : projectThreads.filter((thread) => matches([thread.title]));
      return projectMatches || visibleThreads.length ? [{ key: workspaceKey(workspace), workspace, threads: visibleThreads, threadCount: projectThreads.length }] : [];
    });
    const knownKeys = new Set(workspaceItems.map(workspaceKey));
    const generalThreads = threads.filter((thread) => !thread.workspaceKey && matches([thread.title]));
    const unavailableThreads = threads.filter((thread) => thread.workspaceKey && !knownKeys.has(thread.workspaceKey) && matches([thread.title, thread.workspaceKey]));
    return { projects, generalThreads, unavailableThreads };
  }, [locale, t, threadSearch, threads, workspaceItems]);
  const activeProject = activeThreadWorkspaceKey
    ? workspaceItems.find((workspace) => workspaceKey(workspace) === activeThreadWorkspaceKey)
    : selectedWorkspace;
  const headerProject = activeProject || selectedWorkspace;
  const headerProjectKey = headerProject ? workspaceKey(headerProject) : "";
  const executionBadge = activeExecutionMode === "live" ? "LIVE" : "SAFE";
  const mobileProjectSubtitle = headerProject
    ? `${headerProject.workspaceName} · ${executionBadge} · ${t(workspaceStatusLabel(headerProject))}`
    : `${t("General")} · ${t("No project")}`;
  const contextModeLabel = contextMode === "off" ? t("Off") : contextMode === "aggressive" ? t("Aggressive") : t("Smart");
  const mobileContextSummary = `${selectedWorkspace?.workspaceName || t("General")} · ${mode === "ask" ? "Ask" : mode === "plan" ? "Plan" : "Agent"} · ${labelForModel(selectedModel)} · ${t("Context")}: ${contextModeLabel}`;

  useEffect(() => {
    let cancelled = false;
    if (!headerProject) {
      queueMicrotask(() => {
        if (!cancelled) setActiveExecutionMode("safe");
      });
      return () => { cancelled = true; };
    }
    fetch(runtimeSettingsURL(headerProject), { credentials: "include" })
      .then(async (response) => {
        if (!response.ok) throw new Error(`runtime settings ${response.status}`);
        return response.json() as Promise<unknown>;
      })
      .then((data) => {
        if (cancelled || !isRuntimeSettingsResource(data)) return;
        setActiveExecutionMode(data.effective.executionMode === "live" ? "live" : "safe");
      })
      .catch(() => {
        if (!cancelled) setActiveExecutionMode("safe");
      });
    return () => { cancelled = true; };
  }, [headerProject, headerProjectKey]);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/v1/dashboard/chat/threads", { credentials: "include" })
      .then(async (response) => {
        if (response.status === 401) {
          router.replace("/login");
          return null;
        }
        if (!response.ok) throw new Error(`threads ${response.status}`);
        return response.json() as Promise<{ threads?: ChatThread[] }>;
      })
      .then((data) => {
        if (cancelled || !data) return;
        const available = Array.isArray(data.threads) ? data.threads : [];
        setThreads(available);
        if (!available[0]) {
          setMessages([]);
          setHistoryLoading(false);
          setActiveThreadId(null);
          setNewTaskWorkspaceKey("");
          setNewTaskOpen(true);
          return;
        }
        const initialWorkspaceKey = available[0].workspaceKey;
        if (initialWorkspaceKey) {
          setExpandedProjects((current) => new Set(current).add(initialWorkspaceKey));
        }
        setMessages([]);
        setHistoryLoading(true);
        setActiveThreadId(available[0].id);
      })
      .catch(() => {
        if (!cancelled) setNotice({ kind: "error", text: t("Could not load conversations") });
      })
      .finally(() => {
        if (!cancelled) setThreadActionLoading(false);
      });
    return () => { cancelled = true; };
  }, [router, t]);

  useEffect(() => {
    if (!activeThreadId) {
      return;
    }
    const controller = new AbortController();
    fetch(`/api/v1/dashboard/chat/history?threadId=${encodeURIComponent(activeThreadId)}`, { credentials: "include", signal: controller.signal })
      .then(async (response) => {
        if (response.status === 401) {
          router.replace("/login");
          return null;
        }
        if (!response.ok) throw new Error(`history ${response.status}`);
        return response.json() as Promise<unknown>;
      })
      .then((data) => {
        if (data) setMessages(historyMessages(data));
      })
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === "AbortError")) setNotice({ kind: "error", text: t("Could not load chat history") });
      })
      .finally(() => {
        if (!controller.signal.aborted) setHistoryLoading(false);
      });
    return () => controller.abort();
  }, [activeThreadId, router, t]);

  useEffect(() => {
    if (!activeThreadId) return;
    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      setSelectedModel(activeThreadModel && models.includes(activeThreadModel) ? activeThreadModel : (models.includes("auto") ? "auto" : models[0] || ""));
      const storedWorkspace = activeThreadWorkspaceKey || "auto";
      setSelectedWorkspaceKey(storedWorkspace === "auto" || workspaceItems.some((workspace) => workspaceKey(workspace) === storedWorkspace) ? storedWorkspace : "auto");
    });
    return () => { cancelled = true; };
  }, [activeThreadId, activeThreadModel, activeThreadWorkspaceKey, models, workspaceItems]);

  async function refreshModelCatalog(preserveSelection = true) {
    try {
      const response = await fetch("/api/v1/dashboard/models", { credentials: "include" });
      if (!response.ok) throw new Error(String(response.status));
      const data = (await response.json()) as { models?: string[]; model_options?: ChatModelOption[]; default_model?: string };
      const available = Array.isArray(data.models) ? data.models.filter((model) => typeof model === "string" && model.trim()) : [];
      const options = Array.isArray(data.model_options)
        ? data.model_options.filter((option) => option && typeof option.id === "string" && available.includes(option.id))
        : [];
      setModels(available);
      setModelOptions(options);
      setSelectedModel((current) => preserveSelection && available.includes(current)
        ? current
        : data.default_model && available.includes(data.default_model) ? data.default_model : available[0] || "");
      const selectable = available.filter((model) => model !== "auto");
      setNewTaskModel((current) => selectable.includes(current)
        ? current
        : data.default_model && data.default_model !== "auto" && selectable.includes(data.default_model) ? data.default_model : selectable[0] || "");
    } catch {
      setModels([]);
      setModelOptions([]);
      setSelectedModel("");
      setNewTaskModel("");
    }
  }

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) void refreshModelCatalog(false);
    });
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    if (!followLatestRef.current) return;
    endRef.current?.scrollIntoView({ behavior: "auto", block: "end" });
  }, [messages, loading]);

  function updateScrollFollow() {
    const container = messagesRef.current;
    if (!container) return;
    const nearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 96;
    followLatestRef.current = nearBottom;
    setShowJumpLatest(!nearBottom);
  }

  function jumpToLatest() {
    followLatestRef.current = true;
    setShowJumpLatest(false);
    endRef.current?.scrollIntoView({ behavior: "auto", block: "end" });
  }

  useEffect(() => {
    if (!modelPickerOpen) return;
    const closeOnOutsideClick = (event: PointerEvent) => {
      if (modelPickerRef.current && !modelPickerRef.current.contains(event.target as Node)) {
        setModelPickerOpen(false);
        setModelSearch("");
      }
    };
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        setModelPickerOpen(false);
        setModelSearch("");
      }
    };
    document.addEventListener("pointerdown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [modelPickerOpen]);

  async function sha256Hex(file: File) {
    const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
    return Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("");
  }

  async function uploadImage(file: File): Promise<ChatImageMeta> {
    const sha256 = await sha256Hex(file);
    const presign = await fetch("/api/v1/dashboard/media/presign", {
      method: "POST",
      credentials: "include",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ sha256, contentType: file.type, size: file.size }),
    });
    if (presign.status === 401) {
      router.replace("/login");
      throw new Error("Session expired");
    }
    const prepared = (await presign.json().catch(() => ({}))) as Partial<MediaPrepareResponse> & { error?: string; message?: string };
    if (!presign.ok || !prepared.url || !prepared.imageRef) {
      // Task 4: surface the backend media state directly. media_not_configured
      // means S3/Skill storage is off (multipart fallback still works); other
      // failures keep the explicit backend message for retry guidance.
      if (prepared.error === "media_not_configured") throw new Error("Image upload storage (S3) is not configured; the image can still be sent directly and will only be stored compactly in history");
      throw new Error(prepared.message || prepared.error || "Could not prepare image upload");
    }

    if (prepared.upload?.required) {
      if (!prepared.upload.url) throw new Error("Image upload URL is missing");
      let directUploadOK = false;
      try {
        const headers = new Headers();
        for (const [key, values] of Object.entries(prepared.upload.headers || {})) {
          const lower = key.toLowerCase();
          if (lower === "host" || lower === "content-length") continue;
          for (const value of values) headers.append(key, value);
        }
        if (!headers.has("content-type")) headers.set("content-type", file.type);
        const upload = await fetch(prepared.upload.url, {
          method: prepared.upload.method || "PUT",
          headers,
          body: file,
        });
        directUploadOK = upload.ok;
      } catch {
        directUploadOK = false;
      }

      if (!directUploadOK) {
        const fallback = await fetch("/api/v1/dashboard/media/upload", {
          method: "POST",
          credentials: "include",
          headers: {
            "content-type": file.type,
            "x-codelocal-media-sha256": sha256,
            "x-codelocal-media-size": String(file.size),
          },
          body: file,
        });
        const fallbackData = (await fallback.json().catch(() => ({}))) as { error?: string; message?: string };
        if (!fallback.ok) throw new Error(fallbackData.message || "Could not upload the image to CodeLocal");
      }
    }

    return {
      imageRef: prepared.imageRef,
      sha256,
      contentType: prepared.contentType || file.type,
      size: prepared.size || file.size,
    };
  }

  function prepareImage(file: File) {
    if (!file.type.startsWith("image/")) {
      setNotice({ kind: "error", text: t("Only image files are supported") });
      return;
    }
    if (file.size > 8 * 1024 * 1024) {
      setNotice({ kind: "error", text: t("Images can be up to 8 MB") });
      return;
    }

    if (image?.previewUrl.startsWith("blob:")) URL.revokeObjectURL(image.previewUrl);
    setImage({ previewUrl: URL.createObjectURL(file), file });
    setNotice(null);
  }

  function discardImage() {
    if (image?.previewUrl.startsWith("blob:")) URL.revokeObjectURL(image.previewUrl);
    setImage(null);
    if (fileRef.current) fileRef.current.value = "";
  }

  function onFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (file) void prepareImage(file);
  }

  function onPaste(event: ClipboardEvent) {
    const fileFromClipboard = Array.from(event.clipboardData.files).find((file) => file.type.startsWith("image/"));
    const itemFromClipboard = Array.from(event.clipboardData.items).find((entry) => entry.kind === "file" && entry.type.startsWith("image/"));
    const file = fileFromClipboard ?? itemFromClipboard?.getAsFile();
    if (!file) return;
    event.preventDefault();
    event.stopPropagation();
    void prepareImage(file);
  }

  function queueNextPrompt() {
    const content = input.trim();
    if (!loading || !activeThreadId || !content || image) return;
    setQueuedPrompt({ threadId: activeThreadId, content });
    setInput("");
    setNotice({ kind: "status", text: "Next instruction queued for this task. It will run only if the current response completes successfully." });
  }

  function sendQueuedPrompt() {
    if (loading || historyLoading || threadActionLoading || !queuedPrompt || queuedPrompt.threadId !== activeThreadId) return;
    quickMessageRef.current = queuedPrompt.content;
    setQueuedPrompt(null);
    setRunOutcome("idle");
    window.requestAnimationFrame(() => formRef.current?.requestSubmit());
  }

  useEffect(() => {
    if (!shouldDispatchQueuedPrompt({
      queue: queuedPrompt,
      activeThreadId,
      busy: loading || historyLoading || threadActionLoading,
      runOutcome,
    }) || !queuedPrompt) return;
    const frame = window.requestAnimationFrame(() => {
      quickMessageRef.current = queuedPrompt.content;
      setQueuedPrompt(null);
      setRunOutcome("idle");
      formRef.current?.requestSubmit();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [loading, historyLoading, threadActionLoading, runOutcome, queuedPrompt, activeThreadId]);

  function onComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    if (window.matchMedia("(pointer: coarse)").matches || window.innerWidth <= 820) return;
    event.preventDefault();
    if (loading) queueNextPrompt();
    else formRef.current?.requestSubmit();
  }

  function updateAssistant(index: number, content: string, toolCalls: ToolCall[], skills?: SkillBadge[]) {
    setMessages((current) => {
      const copy = [...current];
      const preservedSkills = skills ?? copy[index]?.skills;
      copy[index] = {
        role: "assistant",
        content,
        tool_calls: [...toolCalls],
        skills: preservedSkills?.length ? [...preservedSkills] : undefined,
      };
      return copy;
    });
  }

  function finishStoppedAssistant(index: number, content: string, toolCalls: ToolCall[]) {
    setMessages((current) => {
      const copy = [...current];
      if (!content.trim() && toolCalls.length === 0) {
        copy.splice(index, 1);
        return copy;
      }
      const preservedSkills = copy[index]?.skills;
      copy[index] = {
        role: "assistant",
        content,
        tool_calls: [...toolCalls],
        skills: preservedSkills?.length ? [...preservedSkills] : undefined,
      };
      return copy;
    });
  }

  function submitQuickMessage(message: string) {
    if (loading || historyLoading || threadActionLoading) return;
    quickMessageRef.current = message;
    formRef.current?.requestSubmit();
  }

  async function createThreadRecord(options: { workspaceKey?: string; model?: string } = {}) {
    const workspaceKey = options.workspaceKey ?? (selectedWorkspaceKey === "auto" ? "" : selectedWorkspaceKey);
    const response = await fetch("/api/v1/dashboard/chat/threads", {
      method: "POST",
      credentials: "include",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(threadCreationPayload(options.model ?? selectedModel, workspaceKey)),
    });
    if (response.status === 401) {
      router.replace("/login");
      throw new Error("Session expired");
    }
    if (!response.ok) throw new Error(`create thread ${response.status}`);
    const data = (await response.json()) as { thread?: ChatThread };
    if (!data.thread) throw new Error("missing thread");
    return data.thread;
  }

  async function refreshThreads(preferredThreadId?: string) {
    const response = await fetch("/api/v1/dashboard/chat/threads", { credentials: "include" });
    if (!response.ok) return;
    const data = (await response.json()) as { threads?: ChatThread[] };
    if (!Array.isArray(data.threads)) return;
    setThreads(data.threads);
    if (preferredThreadId && preferredThreadId !== activeThreadId && data.threads.some((thread) => thread.id === preferredThreadId)) {
      setMessages([]);
      setHistoryLoading(true);
      setActiveThreadId(preferredThreadId);
    }
  }

  async function loadNewTaskExecution(projectKey: string) {
    const requestID = newTaskExecutionRequestRef.current + 1;
    newTaskExecutionRequestRef.current = requestID;
    const workspace = workspaceItems.find((item) => workspaceKey(item) === projectKey);
    if (!workspace) {
      setNewTaskExecutionLoading(false);
      setNewTaskExecutionError(true);
      return;
    }
    setNewTaskExecutionLoading(true);
    setNewTaskExecutionError(false);
    try {
      const response = await fetch(runtimeSettingsURL(workspace), { credentials: "include" });
      if (!response.ok) throw new Error(`runtime settings ${response.status}`);
      const data = await response.json() as unknown;
      if (newTaskExecutionRequestRef.current !== requestID || !isRuntimeSettingsResource(data)) return;
      setNewTaskExecutionMode(data.effective.executionMode === "live" ? "live" : "safe");
      setNewTaskExecutionConfigured(Boolean(data.effective.executionModeConfigured));
    } catch {
      if (newTaskExecutionRequestRef.current === requestID) {
        setNewTaskExecutionMode("safe");
        setNewTaskExecutionConfigured(false);
        setNewTaskExecutionError(true);
      }
    } finally {
      if (newTaskExecutionRequestRef.current === requestID) setNewTaskExecutionLoading(false);
    }
  }

  function openNewTaskDialog(projectKey = "") {
    setNotice(null);
    setNewTaskWorkspaceKey(projectKey);
    setNewTaskModel((current) => activeModels.includes(current)
      ? current
      : activeModels.includes(selectedModel) ? selectedModel : activeModels[0] || "");
    setNewTaskOpen(true);
    if (!projectKey || projectKey === GENERAL_PROJECT_KEY) {
      newTaskExecutionRequestRef.current += 1;
      setNewTaskExecutionMode("safe");
      setNewTaskExecutionConfigured(false);
      setNewTaskExecutionLoading(false);
      setNewTaskExecutionError(false);
      return;
    }
    void loadNewTaskExecution(projectKey);
  }

  function chooseNewTaskProject(projectKey: string) {
    setNewTaskWorkspaceKey(projectKey);
    if (!projectKey || projectKey === GENERAL_PROJECT_KEY) {
      newTaskExecutionRequestRef.current += 1;
      setNewTaskExecutionMode("safe");
      setNewTaskExecutionConfigured(false);
      setNewTaskExecutionLoading(false);
      setNewTaskExecutionError(false);
      return;
    }
    void loadNewTaskExecution(projectKey);
  }

  async function saveWorkspaceExecution(projectKey: string, executionMode: RuntimeExecutionMode) {
    const workspace = workspaceItems.find((item) => workspaceKey(item) === projectKey);
    if (!workspace || !csrf) return false;
    const body = new URLSearchParams({
      csrf,
      scope: "workspace",
      deviceId: workspace.deviceId,
      workspaceId: workspace.workspaceId,
      mode: executionMode,
    });
    const response = await fetch("/api/v1/runtime/settings/execution", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
      body,
    });
    if (response.status === 401) {
      router.replace("/login");
      return false;
    }
    if (!response.ok) return false;
    if (headerProjectKey === projectKey) setActiveExecutionMode(executionMode);
    return true;
  }

  async function startNewTask() {
    if (loading || threadActionLoading || !newTaskWorkspaceKey || !newTaskModel) return;
    const general = newTaskWorkspaceKey === GENERAL_PROJECT_KEY;
    const projectKey = general ? "" : newTaskWorkspaceKey;
    setThreadActionLoading(true);
    setNotice(null);
    try {
      if (!general) {
        const saved = await saveWorkspaceExecution(projectKey, newTaskExecutionMode);
        if (!saved) throw new Error("execution_mode_update_failed");
      }
      const thread = await createThreadRecord({ workspaceKey: projectKey, model: newTaskModel });
      setThreads((current) => [thread, ...current]);
      if (projectKey) setExpandedProjects((current) => new Set(current).add(projectKey));
      setSelectedWorkspaceKey(projectKey || "auto");
      setSelectedModel(newTaskModel);
      setHistoryLoading(true);
      setActiveThreadId(thread.id);
      setMessages([]);
      setInput("");
      discardImage();
      setThreadDrawerOpen(false);
      setContextSheetOpen(false);
      setNewTaskOpen(false);
      if (projectKey) setActiveExecutionMode(newTaskExecutionMode);
      window.requestAnimationFrame(() => composerRef.current?.focus());
    } catch {
      setNotice({ kind: "error", text: t("Could not start the task") });
    } finally {
      setThreadActionLoading(false);
    }
  }

  function newThread() {
    if (loading || threadActionLoading) return;
    setThreadDrawerOpen(false);
    openNewTaskDialog("");
  }

  function newProjectThread(projectKey: string) {
    if (loading || threadActionLoading) return;
    setThreadDrawerOpen(false);
    openNewTaskDialog(projectKey);
  }

  async function renameThread(thread: ChatThread) {
    if (loading || threadActionLoading) return;
    const title = window.prompt(t("Conversation name"), thread.title)?.trim();
    if (!title || title === thread.title) return;
    setThreadActionLoading(true);
    try {
      const response = await fetch(`/api/v1/dashboard/chat/threads/${encodeURIComponent(thread.id)}`, {
        method: "PATCH",
        credentials: "include",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ title }),
      });
      if (!response.ok) throw new Error(String(response.status));
      const data = (await response.json()) as { thread?: ChatThread };
      setThreads((current) => {
        const updated = data.thread || { ...thread, title, updatedAt: Date.now() };
        return [updated, ...current.filter((item) => item.id !== thread.id)];
      });
    } catch {
      setNotice({ kind: "error", text: t("Could not rename the conversation") });
    } finally {
      setThreadActionLoading(false);
    }
  }

  async function deleteThread(thread: ChatThread) {
    if (loading || threadActionLoading || !window.confirm(t("Delete “{title}”?", { title: thread.title }))) return;
    setThreadActionLoading(true);
    try {
      const response = await fetch(`/api/v1/dashboard/chat/threads/${encodeURIComponent(thread.id)}`, { method: "DELETE", credentials: "include" });
      if (!response.ok) throw new Error(String(response.status));
      let remaining = threads.filter((item) => item.id !== thread.id);
      if (remaining.length === 0) {
        const replacement = await createThreadRecord();
        remaining = [replacement];
      }
      setThreads(remaining);
      if (activeThreadId === thread.id) {
        setHistoryLoading(true);
        setActiveThreadId(remaining[0].id);
        setMessages([]);
      }
    } catch {
      setNotice({ kind: "error", text: t("Could not delete the conversation") });
    } finally {
      setThreadActionLoading(false);
    }
  }

  async function send(event: FormEvent) {
    event.preventDefault();
    const quickMessage = quickMessageRef.current;
    quickMessageRef.current = null;
    const text = (quickMessage ?? input).trim();
    if ((!text && !image) || loading || imageUploading || historyLoading || threadActionLoading) return;
    if (!selectedModel || activeModels.length === 0) {
      setNotice({ kind: "error", text: "Add an AI provider and at least one model before sending a message." });
      setProviderManagerOpen(true);
      return;
    }
    let requestThreadId = activeThreadId;
    if (!requestThreadId) {
      setNotice({ kind: "status", text: t("Choose a project before sending your first message.") });
      openNewTaskDialog("");
      return;
    }

    const pendingImage = image;
    let sendImage: ChatImageMeta | undefined;
    let useMultipartFallback = false;
    if (pendingImage) {
      setImageUploading(true);
      setNotice({ kind: "status", text: t("Uploading image…") });
      try {
        sendImage = await uploadImage(pendingImage.file);
      } catch {
        useMultipartFallback = true;
        setNotice({ kind: "status", text: t("Sending image directly…") });
      } finally {
        setImageUploading(false);
      }
    }

    const userMessage: ChatMsg = { role: "user", content: text || t("Analyze this image"), image: pendingImage?.previewUrl };
    const next = [...messages, userMessage];
    const placeholderIndex = next.length;

    setMessages([...next, { role: "assistant", content: "", tool_calls: [] }]);
    // Quick actions/queued turns must not erase a newer unsent composer draft.
    if (!quickMessage) setInput("");
    setImage(null);
    setLoading(true);
    setRunOutcome("running");
    if (fileRef.current) fileRef.current.value = "";

    let streamedContent = "";
    let streamedToolCalls: ToolCall[] = [];
    const controller = new AbortController();
    streamAbortRef.current = controller;
    const finishStoppedResponse = () => {
      setRunOutcome("blocked");
      finishStoppedAssistant(placeholderIndex, streamedContent, streamedToolCalls);
      setNotice({ kind: "status", text: t(streamedContent.trim() || streamedToolCalls.length ? "Stopped responding; received content was preserved." : "Stopped before a response was received.") });
    };
    try {
      const requestId = createChatRequestId();
      const history = messages.slice(-50).map((message) => ({
      role: message.role,
      content: message.content,
      image: message.role === "user" ? message.image : undefined,
    }));
      const payload = {
        requestId,
        threadId: requestThreadId,
        message: text || t("Analyze this image"),
        history,
        model: selectedModel,
        mode,
        contextMode,
        goal: goal.trim() || undefined,
        imageMeta: sendImage ? {
          imageRef: sendImage.imageRef,
          sha256: sendImage.sha256,
          contentType: sendImage.contentType,
          size: sendImage.size,
        } : undefined,
        workspace: selectedWorkspace ? {
          deviceId: selectedWorkspace.deviceId,
          workspaceId: selectedWorkspace.workspaceId,
          workspaceName: selectedWorkspace.workspaceName,
        } : undefined,
      };
      let activeSkills: SkillBadge[] = [];
      let completed = false;
      let lastError: unknown;

      for (let attempt = 0; attempt < chatTransportRetryDelays.length; attempt += 1) {
        if (attempt > 0) {
          setNotice({ kind: "status", text: t("Connection unstable · reconnecting automatically ({attempt}/{total})…", { attempt: attempt + 1, total: chatTransportRetryDelays.length }) });
          await waitForChatRetry(chatTransportRetryDelays[attempt - 1], controller.signal);
        }
        try {
          let requestBody: BodyInit;
          const requestHeaders: HeadersInit = {
            accept: "text/event-stream",
            "x-codelocal-request-id": requestId,
          };
          if (useMultipartFallback && pendingImage) {
            const form = new FormData();
            form.append("payload", JSON.stringify(payload));
            form.append("image", pendingImage.file, pendingImage.file.name || "pasted-image");
            requestBody = form;
          } else {
            requestHeaders["content-type"] = "application/json";
            requestBody = JSON.stringify(payload);
          }

          const response = await fetch("/api/v1/dashboard/chat?stream=1", {
            method: "POST",
            credentials: "include",
            headers: requestHeaders,
            body: requestBody,
            signal: controller.signal,
          });

          if (response.status === 401) {
            router.replace("/login");
            throw new Error("Session expired");
          }
          if (response.status === 429) {
            const data = (await response.json().catch(() => ({}))) as { retry_after?: number };
            throw new Error(`HTTP 429 RATE_LIMIT:${data.retry_after || 60}`);
          }
          if (!response.ok || !response.body) {
            const data = (await response.json().catch(() => ({}))) as { error?: string };
            const prefix = data.error ? `${data.error} · ` : "";
            throw new Error(`${prefix}HTTP ${response.status}`);
          }

          const responseSkills = parseSkillHeader(response.headers.get("x-codelocal-skills"));
          if (responseSkills.length) activeSkills = responseSkills;
          if (activeSkills.length) updateAssistant(placeholderIndex, streamedContent, streamedToolCalls, activeSkills);

          if (!(response.headers.get("content-type") || "").includes("text/event-stream")) {
            const data = (await response.json()) as { reply?: string; tool_calls?: ToolCall[]; error?: string; threadId?: string };
            if (data.error) throw new Error(data.error);
            if (data.threadId) requestThreadId = data.threadId;
            streamedContent = data.reply || "";
            streamedToolCalls = data.tool_calls || [];
            updateAssistant(placeholderIndex, streamedContent, streamedToolCalls, activeSkills);
            completed = true;
            setNotice(null);
            break;
          }

          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let buffer = "";
          let content = "";
          let toolCalls: ToolCall[] = [];
          let sawDone = false;
          streamedContent = content;
          streamedToolCalls = toolCalls;
          if (attempt > 0) updateAssistant(placeholderIndex, "", [], activeSkills);

          while (true) {
            const chunk = await reader.read();
            if (chunk.done) break;
            buffer += decoder.decode(chunk.value, { stream: true });
            const frames = buffer.split("\n\n");
            buffer = frames.pop() || "";

            for (const frame of frames) {
              let eventName = "message";
              let dataText = "";
              for (const line of frame.split("\n")) {
                if (line.startsWith("event:")) eventName = line.slice(6).trim();
                if (line.startsWith("data:")) dataText += line.slice(5).trim();
              }
              if (!dataText) continue;

              let data: StreamData;
              try {
                data = JSON.parse(dataText) as StreamData;
              } catch {
                continue;
              }

              if (eventName === "error") throw new Error(data.error || "The model returned a stream error");
              if (eventName === "delta" && typeof data.delta === "string") {
                content += data.delta;
                streamedContent = content;
                updateAssistant(placeholderIndex, content, toolCalls, activeSkills);
                continue;
              }
              if (eventName === "replace" && typeof data.content === "string") {
                content = data.content;
                streamedContent = content;
                updateAssistant(placeholderIndex, content, toolCalls, activeSkills);
                continue;
              }
              if (eventName === "tool_calls" && Array.isArray(data.tool_calls)) {
                toolCalls = data.tool_calls as ToolCall[];
                streamedToolCalls = toolCalls;
                updateAssistant(placeholderIndex, content, toolCalls, activeSkills);
                continue;
              }
              if (eventName === "tool_delta" && Array.isArray(data.tool_calls)) {
                const deltas = data.tool_calls as Array<{ index: number; name?: string; arguments?: string; id?: string }>;
                for (const delta of deltas) {
                  const existing = toolCalls[delta.index] || { id: delta.id || `tool_${delta.index}`, name: "", arguments: "", status: "running" as const };
                  toolCalls[delta.index] = {
                    ...existing,
                    id: delta.id || existing.id,
                    name: delta.name || existing.name,
                    arguments: delta.arguments ?? existing.arguments,
                  };
                }
                streamedToolCalls = toolCalls;
                updateAssistant(placeholderIndex, content, toolCalls, activeSkills);
                continue;
              }
              if (eventName === "done") {
                sawDone = true;
                if (typeof data.threadId === "string" && data.threadId) requestThreadId = data.threadId;
                if (typeof data.reply === "string") content = data.reply;
                if (Array.isArray(data.tool_calls)) toolCalls = data.tool_calls as ToolCall[];
                streamedContent = content;
                streamedToolCalls = toolCalls;
                updateAssistant(placeholderIndex, content, toolCalls, activeSkills);
              }
            }
          }
          if (controller.signal.aborted) {
            finishStoppedResponse();
            return;
          }
          if (!sawDone) throw new Error("chat_stream_incomplete");
          completed = true;
          setNotice(null);
          break;
        } catch (attemptError) {
          if (controller.signal.aborted) {
            finishStoppedResponse();
            return;
          }
          lastError = attemptError;
          if (!isRetryableChatTransportError(attemptError) || attempt === chatTransportRetryDelays.length - 1) {
            throw attemptError;
          }
        }
      }
      if (!completed && lastError) throw lastError;
      if (completed) {
        // Do not auto-send a queued instruction after an approval or failed tool.
        setRunOutcome(streamedToolCalls.some((tool) => tool.status !== "done") ? "blocked" : "done");
      }
    } catch (error) {
      setRunOutcome("blocked");
      if (controller.signal.aborted) {
        finishStoppedResponse();
        return;
      }
      const message = error instanceof Error ? error.message : String(error);
      const failure = friendlyChatFailure(message, t, translateMessage);
      const content = streamedContent ? `${streamedContent}\n\n${failure}` : failure;
      updateAssistant(placeholderIndex, content, streamedToolCalls);
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null;
      setLoading(false);
      if (requestThreadId) void refreshThreads(requestThreadId);
    }
  }

  function stopStream() {
    const controller = streamAbortRef.current;
    if (!controller || controller.signal.aborted) return;
    setRunOutcome("blocked");
    setNotice({ kind: "status", text: t("Stopping response…") });
    controller.abort("user_stop");
  }

  async function clear(thread?: ChatThread) {
    const target = thread ?? activeThread;
    if (loading || threadActionLoading || !target || !window.confirm(t("Delete all content in “{title}”?", { title: target.title }))) return;
    setThreadActionLoading(true);
    try {
      const response = await fetch(`/api/v1/dashboard/chat/history?threadId=${encodeURIComponent(target.id)}`, { method: "DELETE", credentials: "include" });
      if (!response.ok) throw new Error(String(response.status));
      // Only remove local content after the server confirms deletion.
      if (target.id === activeThreadId) {
        setMessages([]);
        setNotice(null);
      }
    } catch {
      // Preserve the conversation on failure. Do not claim that the server deleted it.
      setNotice({ kind: "error", text: t("Could not delete chat history on the server") });
    } finally {
      setThreadActionLoading(false);
    }
  }

  function toggleProject(key: string) {
    setExpandedProjects((current) => toggleExpandedProject(current, key));
  }

  function selectThread(thread: ChatThread) {
    if (loading || historyLoading || threadActionLoading) return;
    setThreadDrawerOpen(false);
    const targetWorkspaceKey = thread.workspaceKey;
    if (targetWorkspaceKey) setExpandedProjects((current) => new Set(current).add(targetWorkspaceKey));
    if (thread.id === activeThreadId) return;
    setMessages([]);
    setHistoryLoading(true);
    setActiveThreadId(thread.id);
  }

  function closeThreadMenu(event: React.MouseEvent<HTMLButtonElement>) {
    const menu = event.currentTarget.closest("details");
    if (menu instanceof HTMLDetailsElement) menu.open = false;
  }

  function ThreadRow({ thread }: { thread: ChatThread }) {
    return (
      <div className={`${styles.threadItem} ${treeStyles.threadItem} ${thread.id === activeThreadId ? styles.threadItemActive : ""}`}>
        <button className={`${styles.threadSelect} ${treeStyles.threadSelect}`} type="button" onClick={() => selectThread(thread)} disabled={loading || historyLoading || threadActionLoading} title={thread.title}>
          <AppIcon name="chat" size={15} />
          <span>{thread.title}</span>
        </button>
        <details className={treeStyles.threadOverflow}>
          <summary aria-label={t("Options for {title}", { title: thread.title })}><span aria-hidden="true">•••</span></summary>
          <div>
            <button type="button" onClick={(event) => { closeThreadMenu(event); void renameThread(thread); }}>{t("Rename")}</button>
            <button type="button" onClick={(event) => { closeThreadMenu(event); void clear(thread); }}>{t("Clear content")}</button>
            <button type="button" className={treeStyles.threadDeleteAction} onClick={(event) => { closeThreadMenu(event); void deleteThread(thread); }}>{t("Delete task")}</button>
          </div>
        </details>
      </div>
    );
  }

  return (
    <section className={styles.chatWorkspace} aria-label={t("CodeLocal chat workspace")}>
      <button className={`${styles.threadDrawerBackdrop} ${threadDrawerOpen ? styles.threadDrawerBackdropOpen : ""}`} type="button" onClick={() => { setThreadDrawerOpen(false); mobileDrawerTriggerRef.current?.focus(); }} aria-label={t("Close menu")} />
      <aside className={`${styles.threadSidebar} ${treeStyles.threadSidebar} ${threadDrawerOpen ? styles.threadSidebarOpen : ""}`} aria-label={t("Tasks and CodeLocal navigation")} aria-modal={threadDrawerOpen || undefined} role={threadDrawerOpen ? "dialog" : undefined}>
        <ChatSidebarBrand closeRef={threadDrawerCloseRef} onClose={() => { setThreadDrawerOpen(false); mobileDrawerTriggerRef.current?.focus(); }} />
        <button className={`${styles.newThreadButton} ${treeStyles.newThreadButton}`} type="button" onClick={() => { setThreadDrawerOpen(false); void newThread(); }} disabled={loading || threadActionLoading}>
          <AppIcon name="plus" size={17} />
          {t("New task")}
        </button>
        <label className={`${styles.threadSearch} ${treeStyles.threadSearch}`}>
          <AppIcon name="search" size={16} />
          <input value={threadSearch} onChange={(event) => setThreadSearch(event.target.value)} placeholder={t("Search projects and tasks")} aria-label={t("Search projects and tasks")} type="search" />
        </label>
        <div className={`${styles.threadList} ${treeStyles.threadList}`} aria-label={t("Projects and tasks")}>
          {threadTree.projects.length === 0 && threadTree.generalThreads.length === 0 && threadTree.unavailableThreads.length === 0 ? (
            <p className={styles.threadEmpty}>{t("No projects or tasks found.")}</p>
          ) : null}
          {threadTree.projects.map(({ key, workspace, threads: projectThreads, threadCount }) => {
            const open = projectTreeOpen(key, threadSearch, expandedProjects);
            const active = activeThreadWorkspaceKey === key || (!activeThreadWorkspaceKey && selectedWorkspaceKey === key);
            return (
              <section className={`${treeStyles.projectGroup} ${active ? treeStyles.projectGroupActive : ""}`} key={key}>
                <div className={treeStyles.projectHead}>
                  <button className={treeStyles.projectToggle} type="button" onClick={() => toggleProject(key)} aria-expanded={open}>
                    <span className={treeStyles.projectFolder} data-state={workspaceState(workspace)} aria-hidden="true"><AppIcon name="folder" size={17} /><i /></span>
                    <span className={treeStyles.projectCopy}>
                      <strong title={workspace.workspaceName}>{workspace.workspaceName}</strong>
                      <small title={`${workspace.deviceName} · ${t(workspaceStatusLabel(workspace))}`}>{workspace.deviceName} · {t(workspaceStatusLabel(workspace))}</small>
                    </span>
                    <span className={treeStyles.projectThreadCount} aria-label={t("{count} tasks", { count: threadCount })}>{threadCount}</span>
                    <AppIcon className={treeStyles.projectChevron} name="chevron-right" size={14} />
                  </button>
                  <button
                    className={treeStyles.projectNewThread}
                    type="button"
                    aria-label={t("Create task in {name}", { name: workspace.workspaceName })}
                    title={t("Create task in {name}", { name: workspace.workspaceName })}
                    disabled={loading || threadActionLoading}
                    onClick={() => void newProjectThread(key)}
                  >
                    <AppIcon name="plus" size={16} />
                  </button>
                </div>
                {open ? (
                  <div className={treeStyles.projectThreads}>
                    {projectThreads.length ? projectThreads.map((thread) => <ThreadRow thread={thread} key={thread.id} />) : (
                      <button className={treeStyles.emptyProjectAction} type="button" onClick={() => void newProjectThread(key)} disabled={loading || threadActionLoading}>
                        <AppIcon name="plus" size={14} /> {t("Create first task")}
                      </button>
                    )}
                  </div>
                ) : null}
              </section>
            );
          })}
          {threadTree.generalThreads.length ? (
            <section className={`${treeStyles.projectGroup} ${!activeThreadWorkspaceKey ? treeStyles.projectGroupActive : ""}`}>
              <div className={treeStyles.generalGroupHead}><AppIcon name="folder" size={15} /><span><strong>{t("General")}</strong><small>{t("No project")}</small></span><b>{threadTree.generalThreads.length}</b></div>
              <div className={treeStyles.projectThreads}>{threadTree.generalThreads.map((thread) => <ThreadRow thread={thread} key={thread.id} />)}</div>
            </section>
          ) : null}
          {threadTree.unavailableThreads.length ? (
            <section className={treeStyles.projectGroup}>
              <div className={treeStyles.generalGroupHead}><AppIcon name="folder" size={15} /><span><strong>{t("Unavailable projects")}</strong><small>{t("The workspace is no longer in the list")}</small></span><b>{threadTree.unavailableThreads.length}</b></div>
              <div className={treeStyles.projectThreads}>{threadTree.unavailableThreads.map((thread) => <ThreadRow thread={thread} key={thread.id} />)}</div>
            </section>
          ) : null}
        </div>
        <ChatSidebarFooter onNavigate={() => setThreadDrawerOpen(false)} />
      </aside>

      <section ref={chatFrameRef} className={styles.chatShell} aria-label={t("Chat with CodeLocal")} aria-hidden={contextSheetOpen || threadDrawerOpen || newTaskOpen || undefined}>
        <ChatTopBar
          drawerOpen={threadDrawerOpen}
          title={activeThread?.title || t("New task")}
          subtitle={mobileProjectSubtitle}
          disabled={loading || threadActionLoading}
          menuRef={mobileDrawerTriggerRef}
          onOpenMenu={() => setThreadDrawerOpen(true)}
          onOpenTaskSetup={() => openNewTaskDialog(headerProjectKey || GENERAL_PROJECT_KEY)}
          onNewThread={() => void newThread()}
        />
        <div className={styles.chatHead}>
          <div className={headerStyles.threadHeadContext}>
            <button ref={threadDrawerTriggerRef} className={styles.threadDrawerToggle} type="button" onClick={() => setThreadDrawerOpen(true)} aria-label={t("Open CodeLocal menu")} aria-expanded={threadDrawerOpen}>
              <AppIcon name="menu" size={19} />
            </button>
            <div>
              <h1 title={activeThread?.title || t("New task")}>{activeThread?.title || t("New task")}</h1>
              <span title={mobileProjectSubtitle}>{headerProject?.workspaceName || t("General")}</span>
            </div>
          </div>
          <div className={styles.chatActions}>
            <button
              className={`${styles.projectPicker} ${styles.contextChip}`}
              type="button"
              onClick={() => openNewTaskDialog(headerProjectKey || GENERAL_PROJECT_KEY)}
              disabled={loading || threadActionLoading}
              title={t("Switch project by starting a new task")}
              aria-label={t("Switch project by starting a new task")}
            >
              <span className={styles.projectPickerIcon}><AppIcon name={headerProject ? "folder" : "chat"} size={16} /></span>
              <span className={styles.contextChipLabel}>{headerProject?.workspaceName || t("General")}</span>
              <span className={styles.modelPickerChevron} aria-hidden="true">⌄</span>
            </button>
            {headerProject ? (
              <button
                className={`${styles.executionModeChip} ${activeExecutionMode === "live" ? styles.executionModeChipLive : ""}`}
                type="button"
                onClick={() => openNewTaskDialog(headerProjectKey)}
                disabled={loading || threadActionLoading}
                title={t("Change execution mode by starting a new task")}
                aria-label={t("Change execution mode by starting a new task")}
              >
                <AppIcon name={activeExecutionMode === "live" ? "runtime" : "shield"} size={15} />
                <span>{activeExecutionMode === "live" ? "LIVE" : t("Safe Workspace")}</span>
              </button>
            ) : null}
            <label className={styles.contextModePicker} title={t("Context optimization")}>
              <span>{t("Context")}</span>
              <select value={contextMode} onChange={(event) => setContextMode(event.target.value as ChatContextMode)} aria-label={t("Context optimization")} disabled={loading}>
                <option value="smart">{t("Smart")}</option>
                <option value="off">{t("Off")}</option>
                <option value="aggressive">{t("Aggressive")}</option>
              </select>
            </label>
            <div className={styles.modelPickerShell} ref={modelPickerRef}>
              <button
                className={`${styles.projectPicker} ${styles.modelPicker}`}
                type="button"
                aria-label={t("Select model")}
                aria-haspopup="listbox"
                aria-expanded={modelPickerOpen}
                onClick={() => setModelPickerOpen((open) => !open)}
              >
                <span className={styles.modelPickerName}>{selectedModel ? labelForModel(selectedModel) : "No AI model"}</span>
                {activeModels.length > 0 ? <span className={styles.modelPickerCount}>{activeModels.length}</span> : null}
                <span className={styles.modelPickerChevron} aria-hidden="true">⌄</span>
              </button>
              {modelPickerOpen ? (
                <div className={styles.modelPickerMenu} role="dialog" aria-label={t("Find and select a model")}>
                  <label className={styles.modelSearch}>
                    <AppIcon name="search" size={15} />
                    <input
                      autoFocus
                      value={modelSearch}
                      onChange={(event) => setModelSearch(event.target.value)}
                      placeholder={t("Search GPT, Claude, Gemini...")}
                      aria-label={t("Search models")}
                    />
                  </label>
                  <div className={styles.modelPickerSummary}>{t("{count} active models", { count: activeModels.length })}</div>
                  <div className={styles.modelOptionList} role="listbox" aria-label={t("Active models")}>
                    {!modelSearch.trim() && models.includes("auto") ? (
                      <button
                        className={`${styles.modelOption} ${selectedModel === "auto" ? styles.modelOptionActive : ""}`}
                        type="button"
                        role="option"
                        aria-selected={selectedModel === "auto"}
                        onClick={() => {
                          setSelectedModel("auto");
                          setModelPickerOpen(false);
                          setModelSearch("");
                        }}
                      >
                        <span>Auto</span>
                        <small>{t("Automatically choose the default model")}</small>
                      </button>
                    ) : null}
                    {visibleModels.map((model) => (
                      <button
                        className={`${styles.modelOption} ${selectedModel === model ? styles.modelOptionActive : ""}`}
                        type="button"
                        role="option"
                        aria-selected={selectedModel === model}
                        key={model}
                        onClick={() => {
                          setSelectedModel(model);
                          setModelPickerOpen(false);
                          setModelSearch("");
                        }}
                      >
                        <span>{labelForModel(model)}</span>
                        <small>{modelOptionMap.get(model)?.custom ? `${providerForModel(model)} · ${model}` : providerForModel(model)}</small>
                      </button>
                    ))}
                    {visibleModels.length === 0 ? <p className={styles.modelEmpty}>{activeModels.length === 0 ? "No AI model configured yet." : t("No active models found.")}</p> : null}
                  </div>
                  <button
                    className={styles.manageAIButton}
                    type="button"
                    onClick={() => {
                      setModelPickerOpen(false);
                      setModelSearch("");
                      setProviderManagerOpen(true);
                    }}
                  >
                    <AppIcon name="plus" size={15} />
                    <span><strong>{t("Add or manage AI providers")}</strong><small>{t("Use your own API keys and models")}</small></span>
                    <AppIcon name="chevron-right" size={14} />
                  </button>
                </div>
              ) : null}
            </div>
          </div>
        </div>

        <div ref={messagesRef} className={styles.chatMessages} onPaste={onPaste} onScroll={updateScrollFollow}>
          {historyLoading ? <div className={styles.historyLoading}>{t("Loading conversation…")}</div> : messages.length === 0 ? (
            <div className={styles.emptyState}>
              <span className={styles.emptyOrb} aria-hidden="true"><AppIcon name="codelocal" size={27} /></span>
              <strong>{t("Start a task")}</strong>
              <p>{t("Describe what needs to be done. CodeLocal will read the project, execute the task, and verify the result.")}</p>
              <div className={styles.suggestions}>
                {suggestions.map((suggestion) => <button key={suggestion} type="button" onClick={() => setInput(t(suggestion))}>{t(suggestion)}</button>)}
              </div>
            </div>
          ) : messages.map((message, index) => (
            <div key={`${message.role}-${index}`} className={`${styles.msgBlock} ${message.role === "user" ? styles.userBlock : styles.assistantBlock}`}>
              <div className={styles.messageBody}>
                {message.skills?.length ? (
                  <div className={skillStyles.list} aria-label={t("Skills used by CodeLocal")}>
                    {message.skills.map((skill) => (
                      <span className={skillStyles.pill} key={`${skill.id}@${skill.version}`} title={t("CodeLocal selected {name}@{version} for this task", { name: skill.name, version: skill.version })}>
                        <AppIcon name="skill" size={13} />
                        {skill.name}
                      </span>
                    ))}
                  </div>
                ) : null}
                {message.tool_calls?.length ? (
                  <ChatActionSummary actions={message.tool_calls} busy={loading} onApprove={(access) => submitQuickMessage(access)} />
                ) : null}
                {message.image ? <img src={message.image} alt={t("Sent image")} className={styles.msgImage} /> : null}
                {message.content ? (
                  <div className={`${styles.msg} ${message.role === "user" ? styles.msgUser : styles.msgAssistant} ${loading && message.role === "assistant" && index === messages.length - 1 ? styles.msgStreaming : ""}`}>
                    {message.role === "assistant" ? <ChatRichMessage content={message.content} /> : message.content}
                    {loading && message.role === "assistant" && index === messages.length - 1 ? (
                      <span className={styles.streamingDots} aria-label={t("CodeLocal is still responding")}><i /><i /><i /></span>
                    ) : null}
                  </div>
                ) : loading && index === messages.length - 1 ? <div className={styles.thinking} aria-label={t("CodeLocal is responding")}><i /><i /><i /></div> : null}
              </div>
            </div>
          ))}
          <div ref={endRef} />
        </div>

        {showJumpLatest ? <button className={mobileStyles.jumpLatest} type="button" onClick={jumpToLatest}><AppIcon name="chevron-down" size={16} /> {t("Latest")}</button> : null}

        {queuedPrompt && queuedPrompt.threadId === activeThreadId ? (
          <div className={styles.queuedPrompt} role="status">
            <AppIcon name="check" size={15} />
            <span><strong>Next instruction</strong><small title={queuedPrompt.content}>{queuedPrompt.content}</small></span>
            {!loading ? <button type="button" onClick={sendQueuedPrompt}>Send now</button> : null}
            <button type="button" onClick={() => setQueuedPrompt(null)} aria-label="Remove queued instruction"><AppIcon name="close" size={15} /></button>
          </div>
        ) : null}
        <form ref={formRef} className={styles.chatForm} onSubmit={send} onPaste={onPaste}>
          <input ref={fileRef} type="file" accept="image/*" onChange={onFile} className={styles.fileInput} />
          {image ? <div className={styles.imagePreview}><img src={image.previewUrl} alt={t("Image ready to send")} /><button type="button" onClick={discardImage} aria-label={t("Remove image")}><AppIcon name="close" size={14} /></button></div> : null}
          <textarea ref={composerRef} value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={onComposerKeyDown} onPaste={onPaste} placeholder={t("Give CodeLocal a task…")} aria-label={t("Describe the task")} enterKeyHint="enter" rows={1} />
          <div className={styles.composerToolbar}>
            <div className={styles.composerOptions}>
              <button ref={contextTriggerRef} type="button" className={mobileStyles.contextTrigger} onClick={() => setContextSheetOpen(true)} aria-label={t("Add image or adjust context")} aria-haspopup="dialog" aria-expanded={contextSheetOpen} disabled={loading || imageUploading}>
                <AppIcon name="plus" size={20} />
              </button>
              <span className={mobileStyles.contextSummary} title={mobileContextSummary}>{mobileContextSummary}</span>
              <button type="button" className={styles.attachBtn} onClick={() => fileRef.current?.click()} aria-label={t("Attach image")} disabled={loading || imageUploading}>
                <AppIcon name="paperclip" size={17} />
              </button>
              {headerProject ? (
                <button type="button" className={styles.attachBtn} onClick={() => submitQuickMessage("Inspect the current project's real Git status and unified diff using get_project_git_status and review_project_diff. Summarize changed files, do not modify anything.")} title="Review project changes (read-only)" aria-label="Review Git changes" disabled={loading || historyLoading || threadActionLoading || !activeThreadId || activeModels.length === 0}>
                  <AppIcon name="code" size={17} />
                </button>
              ) : null}
              <label className={styles.modePicker} title={mode === "agent" ? t("Agent can edit files and run commands") : t("{mode} uses read-only tools", { mode: mode === "ask" ? "Ask" : "Plan" })}>
                <select value={mode} onChange={(event) => setMode(event.target.value as ChatMode)} aria-label={t("Choose chat mode")} disabled={loading}>
                  <option value="ask">Ask</option>
                  <option value="plan">Plan</option>
                  <option value="agent">Agent</option>
                </select>
              </label>
              <label className={`${styles.goalField} ${goal ? styles.goalFieldActive : ""}`}>
                <AppIcon name="target" size={13} />
                <input value={goal} onChange={(event) => setGoal(event.target.value)} placeholder={t("Add goal")} aria-label={t("Goal")} maxLength={240} disabled={loading} />
                {goal ? <button className={styles.goalClear} type="button" onClick={() => setGoal("")} aria-label={t("Clear goal")} disabled={loading}><AppIcon name="close" size={11} /></button> : null}
              </label>
            </div>
            {loading && input.trim() && !image ? (
              <button className={styles.queueButton} type="button" onClick={queueNextPrompt} title="Queue the next instruction for this task" aria-label="Queue next instruction">
                <AppIcon name="plus" size={15} /> <span>Queue</span>
              </button>
            ) : null}
            <button className={`${styles.sendBtn} ${loading ? styles.stopBtn : ""}`} type={loading ? "button" : "submit"} onClick={loading ? stopStream : undefined} disabled={loading ? false : imageUploading || historyLoading || threadActionLoading || activeModels.length === 0 || (!input.trim() && !image)} aria-label={loading ? t("Stop response") : t("Send")} title={loading ? t("Stop response") : activeModels.length === 0 ? "Add an AI provider first" : t("Send")}>
              <AppIcon name={loading ? "stop" : "send"} size={loading ? 16 : 18} />
            </button>
          </div>
        </form>
        {notice ? <div className={styles.chatHint} role={notice.kind === "error" ? "alert" : "status"}>{notice.text}</div> : null}
      </section>
      <ChatNewTaskDialog
        open={newTaskOpen}
        projects={newTaskProjects}
        selectedProjectKey={newTaskWorkspaceKey}
        executionMode={newTaskExecutionMode}
        executionConfigured={newTaskExecutionConfigured}
        executionLoading={newTaskExecutionLoading}
        executionError={newTaskExecutionError}
        models={newTaskModels}
        selectedModel={newTaskModel}
        busy={threadActionLoading}
        onProjectChange={chooseNewTaskProject}
        onExecutionModeChange={(value) => {
          setNewTaskExecutionMode(value);
          setNewTaskExecutionConfigured(false);
        }}
        onModelChange={setNewTaskModel}
        onClose={() => {
          if (!threadActionLoading) setNewTaskOpen(false);
        }}
        onStart={() => void startNewTask()}
      />
      <ChatContextSheet
        open={contextSheetOpen}
        triggerRef={contextTriggerRef}
        imageDisabled={loading || imageUploading}
        workspaceItems={workspaceItems}
        selectedWorkspaceKey={selectedWorkspaceKey}
        mode={mode}
        models={models}
        selectedModel={selectedModel}
        contextMode={contextMode}
        goal={goal}
        modelLabel={labelForModel}
        workspaceKey={workspaceKey}
        workspaceStatusLabel={(workspace) => t(workspaceStatusLabel(workspace))}
        onClose={() => setContextSheetOpen(false)}
        onAttach={() => { setContextSheetOpen(false); window.requestAnimationFrame(() => fileRef.current?.click()); }}
        onWorkspaceChange={(value) => {
          if (value === selectedWorkspaceKey) return;
          setContextSheetOpen(false);
          openNewTaskDialog(value === "auto" ? GENERAL_PROJECT_KEY : value);
        }}
        onModeChange={setMode}
        onModelChange={setSelectedModel}
        onContextModeChange={setContextMode}
        onManageProviders={() => { setContextSheetOpen(false); setProviderManagerOpen(true); }}
        onGoalChange={setGoal}
      />
      <ChatProviderManager
        open={providerManagerOpen}
        onClose={() => setProviderManagerOpen(false)}
        onChanged={() => void refreshModelCatalog(true)}
      />
    </section>
  );
}

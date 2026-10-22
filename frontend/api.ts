// API 服务层 - 连接后端接口

const isBrowser = typeof window !== 'undefined';
const shouldForceHttps = isBrowser && window.location.protocol === 'https:';

export const ensureHttpsUrl = (url?: string | null): string => {
  if (!url) return '';
  if (!shouldForceHttps) return url;
  if (!url.startsWith('http:')) return url;
  try {
    const parsed = new URL(url);
    parsed.protocol = 'https:';
    return parsed.toString();
  } catch {
    return url.replace(/^http:/, 'https:');
  }
};

const API_BASE_URL = ensureHttpsUrl(import.meta.env.VITE_API_URL || 'http://localhost:8080');
const FILE_API_PREFIX = '/api/files/';
export const MAX_UPLOAD_IMAGE_EDGE = 6000;
export const MIN_UPLOAD_AUDIO_DURATION = 5; // 秒
export const MAX_UPLOAD_AUDIO_DURATION = 12; // 秒
const API_HOST = (() => {
  try {
    return new URL(API_BASE_URL).host;
  } catch {
    return '';
  }
})();

/**
 * 检查 URL 是否可以直接用于 img/video 标签的 src 属性
 * 只有完整的 http(s) URL、data URL 或 blob URL 才是有效的
 * 文件 key（如 "abc123"）不能直接使用，会被浏览器解析为相对路径
 */
export const isValidMediaUrl = (url?: string | null): boolean => {
  if (!url) return false;
  return (
    url.startsWith('http://') ||
    url.startsWith('https://') ||
    url.startsWith('data:') ||
    url.startsWith('blob:')
  );
};

/**
 * 下载文件：通过 ?download=true 触发后端返回 Content-Disposition: attachment
 */
export const downloadFile = async (url: string, _filename?: string) => {
  const separator = url.includes('?') ? '&' : '?';
  const downloadUrl = `${url}${separator}download=true`;
  const link = document.createElement('a');
  link.href = downloadUrl;
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
};

export const normalizeFileKey = (input?: string | null): { key: string | null; externalUrl?: string } => {
  if (!input) return { key: null };
  const normalized = ensureHttpsUrl(typeof input === 'string' ? input : String(input));
  if (normalized.startsWith('data:') || normalized.startsWith('blob:')) {
    return { key: null, externalUrl: normalized };
  }
  const idx = normalized.lastIndexOf(FILE_API_PREFIX);
  if (idx >= 0) {
    const key = normalized.slice(idx + FILE_API_PREFIX.length).replace(/^\//, '');
    return { key: key || null };
  }
  if (/^https?:\/\//.test(normalized)) {
    try {
      const parsed = new URL(normalized);
      const pathKey = parsed.pathname.replace(/^\//, '');
      const isSameOrigin = isBrowser && parsed.origin === window.location.origin;
      const isApiHost = API_HOST && parsed.host === API_HOST;
      if (isSameOrigin || isApiHost) {
        return { key: pathKey || null };
      }
      return { key: null, externalUrl: normalized };
    } catch {
      return { key: null, externalUrl: normalized };
    }
  }
  return { key: normalized.replace(/^\//, '') };
};

// 后端 Book 类型定义
export type AdaptationStatus = 'NONE' | 'IN_PROGRESS' | 'COMPLETED' | 'ARCHIVED';

export interface Book {
  id: number;
  title: string;
  author: string;
  cover: string;
  coverLandscape: string;
  description: string;
  outline: string;
  artStyle: string;
  originalTextKey?: string;
  originalTextPreview?: string;
  adaptationStatus: AdaptationStatus;
  adaptedBy: string;
  chapterCount: number;
  isFavorite: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface BookListResponse {
  total: number;
  page: number;
  size: number;
  data: Book[];
}

export interface CreateBookRequest {
  title: string;
  author: string;
  cover?: string;
  coverLandscape?: string;
  description?: string;
  outline?: string;
  artStyle?: string;
  originalTextKey?: string;
  originalTextPreview?: string;
}

export interface BookListParams {
  page?: number;
  size?: number;
  keyword?: string;
  status?: string; // 逗号分隔多状态，如 "NONE,IN_PROGRESS"
  favorite?: boolean;
}

// API 错误处理
export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
    this.name = 'ApiError';
  }
}

const TOKEN_KEY = 'auth_token';

export const authStorage = {
  getToken: () => localStorage.getItem(TOKEN_KEY),
  setToken: (token: string) => localStorage.setItem(TOKEN_KEY, token),
  clear: () => localStorage.removeItem(TOKEN_KEY),
};

/** 会话确认失效（携带 token 的请求收到 401）时广播该事件，由应用统一登出 */
const AUTH_EXPIRED_EVENT = 'auth:expired';

const notifyAuthExpired = () => {
  if (isBrowser) window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
};

/** 判断错误是否为 401 未授权（会话失效），区别于网络/服务端瞬时错误 */
export const isUnauthorizedError = (err: unknown): boolean =>
  err instanceof ApiError && err.status === 401;

async function request<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const url = `${API_BASE_URL}${endpoint}`;

  const isFormData = options?.body instanceof FormData;
  const headers: Record<string, string> = {
    ...(options?.headers as Record<string, string>),
  };
  if (!isFormData) {
    headers['Content-Type'] = headers['Content-Type'] || 'application/json';
  }
  const hasAuthHeader = Object.prototype.hasOwnProperty.call(headers, 'Authorization');
  if (!hasAuthHeader) {
    const token = authStorage.getToken();
    if (token) headers.Authorization = `Bearer ${token}`;
  }
  // 是否实际携带了 token（登录/注册显式传空 Authorization，不算携带）
  const carriedToken = Boolean(headers.Authorization);

  const response = await fetch(url, {
    ...options,
    headers,
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({}));
    // 携带 token 的请求收到 401 = 会话已失效，广播统一登出
    if (response.status === 401 && carriedToken) notifyAuthExpired();
    throw new ApiError(response.status, errorData.error || `Request failed with status ${response.status}`);
  }

  return response.json();
}

// 认证 API
export interface AuthResponse {
  token: string;
  user: {
    id: number;
    username: string;
    nickname: string;
  };
}

export const authApi = {
  login: (payload: { username: string; password: string }) =>
    request<AuthResponse>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify(payload),
      headers: { Authorization: '' }, // 显式移除 token
    }),
  register: (payload: { username: string; password: string; nickname?: string }) =>
    request<AuthResponse>('/api/auth/register', {
      method: 'POST',
      body: JSON.stringify(payload),
      headers: { Authorization: '' }, // 显式移除 token
    }),
  me: () => request<AuthResponse['user']>('/api/auth/me'),
  // 登出：删除当前会话（仅当前端，不影响其他设备）
  // 传入 token 用于登出前 localStorage 已被清空的场景
  logout: (token?: string) =>
    request<{ message: string }>('/api/auth/logout', {
      method: 'POST',
      ...(token ? { headers: { Authorization: `Bearer ${token}` } } : {}),
    }),
};

// 书籍 API
export const bookApi = {
  // 获取书籍列表
  list: async (params?: BookListParams): Promise<BookListResponse> => {
    const searchParams = new URLSearchParams();
    if (params?.page) searchParams.set('page', String(params.page));
    if (params?.size) searchParams.set('size', String(params.size));
    if (params?.keyword) searchParams.set('keyword', params.keyword);
    if (params?.status) searchParams.set('status', params.status);
    if (params?.favorite) searchParams.set('favorite', 'true');

    const queryString = searchParams.toString();
    const endpoint = `/api/books${queryString ? `?${queryString}` : ''}`;

    return request<BookListResponse>(endpoint);
  },

  // 创建书籍
  create: async (data: CreateBookRequest): Promise<Book> => {
    return request<Book>('/api/books', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },

  // 获取单个书籍详情
  getById: async (id: number): Promise<Book> => {
    return request<Book>(`/api/books/${id}`);
  },

  // 更新书籍
  update: async (id: number, data: CreateBookRequest): Promise<Book> => {
    return request<Book>(`/api/books/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  },

  // 删除书籍
  delete: async (id: number): Promise<void> => {
    return request(`/api/books/${id}`, {
      method: 'DELETE',
    });
  },

  // 归档书籍
  archive: async (id: number): Promise<Book> => {
    return request<Book>(`/api/books/${id}/archive`, {
      method: 'PUT',
    });
  },

  // 取消归档
  unarchive: async (id: number): Promise<Book> => {
    return request<Book>(`/api/books/${id}/unarchive`, {
      method: 'PUT',
    });
  },

  favorite: async (id: number): Promise<{ isFavorite: boolean }> => {
    return request<{ isFavorite: boolean }>(`/api/books/${id}/favorite`, {
      method: 'PUT',
    });
  },

  unfavorite: async (id: number): Promise<{ isFavorite: boolean }> => {
    return request<{ isFavorite: boolean }>(`/api/books/${id}/favorite`, {
      method: 'DELETE',
    });
  },

  // 更新大纲
  updateOutline: async (id: number, outline: string): Promise<Book> => {
    return request<Book>(`/api/books/${id}/outline`, {
      method: 'PUT',
      body: JSON.stringify({ outline }),
    });
  },

  // 更新画风提示词
  updateArtStyle: async (id: number, artStyle: string): Promise<Book> => {
    return request<Book>(`/api/books/${id}/art-style`, {
      method: 'PUT',
      body: JSON.stringify({ artStyle }),
    });
  },
};

// 工具函数：将后端 Book 转换为前端 Project 格式
import {
  Comment,
  CommentListResponse,
  CommentModule,
  CommentStatus,
  Project,
  Status,
  Episode,
  Scene,
  SceneReference,
  ChapterVideo,
  ChapterVideoVersion,
  VideoStatus,
  SceneFrameSet,
  SceneFrameSetVersion,
  SceneAnimation,
  SceneAnimationGenerationTask,
  SceneAnimationVersion,
  Character,
  SceneAsset,
  Lora,
  LoraListResponse,
  LoraListParams,
  CreateLoraRequest,
  UpdateLoraRequest,
} from './types';

export function bookToProject(book: Book): Project {
  // 将后端的 adaptationStatus 映射到前端的 productionStatus
  const statusMap: Record<AdaptationStatus, Status> = {
    'NONE': 'DRAFT',
    'IN_PROGRESS': 'IN_PROGRESS',
    'COMPLETED': 'COMPLETED',
    'ARCHIVED': 'ARCHIVED',
  };

  return {
    id: book.id,
    title: book.title,
    author: book.author,
    cover: book.cover || 'https://images.unsplash.com/photo-1518709268805-4e9042af9f23?q=80&w=1000&auto=format&fit=crop',
    coverLandscape: book.coverLandscape || '',
    productionStatus: statusMap[book.adaptationStatus],
    episodes: [], // 初始化为空数组，后续可以从其他 API 获取
    assignedWriter: book.adaptedBy || undefined,
    outline: book.outline || '',
    originalTextKey: book.originalTextKey || '',
    originalTextPreview: book.originalTextPreview || '',
    isFavorite: Boolean(book.isFavorite),
  };
}

export function booksToProjects(books: Book[]): Project[] {
  return books.map(bookToProject);
}

// 章节/场景 API
export interface Chapter {
  id: number;
  bookId: number;
  title: string;
  index: number;
  status: Status;
  synopsis?: string;
  scenes?: Scene[];
}

export type ChapterImportTaskStatus = 'PENDING' | 'ANALYZING' | 'IMPORTING' | 'SUCCEEDED' | 'FAILED';

export interface ChapterImportTask {
  id: number;
  bookId: number;
  status: ChapterImportTaskStatus;
  originalFilename: string;
  model: string;
  outputChapterId?: number;
  errorMessage?: string;
  attemptCount: number;
  startedAt?: string;
  completedAt?: string;
  createdBy: number;
  createdAt: string;
  updatedAt: string;
}

export interface ScenePayload {
  description: string;
  cameraMovement: string;
  dialogue: string;
  sceneAssetCode?: string | null;
  transitionEffect?: string; // 转场或剪辑手法
  referenceImageUrl?: string;
  referenceImageDescription?: string; // 参考图说明
  thumbnailUrl?: string;
}

export const chapterApi = {
  list: (bookId: number, includeScenes = true) =>
    request<{ total: number; data: Chapter[] }>(`/api/books/${bookId}/chapters?includeScenes=${includeScenes}`),
  get: (bookId: number, chapterId: number, includeScenes = true) =>
    request<Chapter>(`/api/books/${bookId}/chapters/${chapterId}?includeScenes=${includeScenes}`),
  create: (bookId: number, payload: { title: string; index: number; status?: Status; synopsis?: string }) =>
    request<Chapter>(`/api/books/${bookId}/chapters`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  import: (bookId: number, file: File) => {
    const formData = new FormData();
    formData.append('file', file);
    return request<ChapterImportTask>(`/api/books/${bookId}/chapters/import`, {
      method: 'POST',
      body: formData,
    });
  },
  listImportTasks: (bookId: number) =>
    request<{ total: number; data: ChapterImportTask[] }>(`/api/books/${bookId}/chapters/import-tasks`),
  getImportTask: (bookId: number, taskId: number) =>
    request<ChapterImportTask>(`/api/books/${bookId}/chapters/import-tasks/${taskId}`),
  update: (bookId: number, chapterId: number, payload: Partial<{ title: string; index: number; status: Status; synopsis?: string }>) =>
    request<Chapter>(`/api/books/${bookId}/chapters/${chapterId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (bookId: number, chapterId: number) =>
    request(`/api/books/${bookId}/chapters/${chapterId}`, { method: 'DELETE' }),
};

export const sceneApi = {
  create: (bookId: number, chapterId: number, payload: { index: number } & ScenePayload & { status?: Status }) =>
    request<Scene>(`/api/books/${bookId}/chapters/${chapterId}/scenes`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (bookId: number, chapterId: number, sceneId: number, payload: Partial<{ index: number; status: Status } & ScenePayload>) =>
    request<Scene>(`/api/books/${bookId}/chapters/${chapterId}/scenes/${sceneId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (bookId: number, chapterId: number, sceneId: number) =>
    request(`/api/books/${bookId}/chapters/${chapterId}/scenes/${sceneId}`, { method: 'DELETE' }),
};

// 文件上传 API（用于参考图等）
export interface FileUploadResponse {
  id: number;
  key: string;
  originalName: string;
  size: number;
  mimeType: string;
  uploaderId: number;
  visibility: 'public' | 'private';
  url: string;
  createdAt: string;
}

export interface OriginalTextUploadResponse extends FileUploadResponse {
  preview: string;
}

const IMAGE_OUTPUT_EXTENSIONS: Record<string, string> = {
  'image/jpeg': 'jpg',
  'image/png': 'png',
  'image/webp': 'webp',
};

const getImageResizeOutputType = (file: File): string => {
  if (file.type === 'image/jpeg' || file.type === 'image/webp') return file.type;
  return 'image/webp';
};

const withImageExtension = (filename: string, mimeType: string): string => {
  const extension = IMAGE_OUTPUT_EXTENSIONS[mimeType] || 'webp';
  const basename = filename.replace(/\.[^/.]+$/, '');
  return `${basename || 'image'}.${extension}`;
};

const canvasToBlob = (canvas: HTMLCanvasElement, mimeType: string): Promise<Blob> =>
  new Promise((resolve, reject) => {
    canvas.toBlob(
      blob => {
        if (!blob) {
          reject(new Error('图片压缩失败'));
          return;
        }
        resolve(blob);
      },
      mimeType,
      0.92
    );
  });

const loadUploadImage = (file: File): Promise<HTMLImageElement> =>
  new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const image = new Image();
    image.onload = () => {
      URL.revokeObjectURL(url);
      resolve(image);
    };
    image.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error('图片读取失败'));
    };
    image.src = url;
  });

/**
 * 统一的上传前文件处理入口：
 * - 非图片原样上传
 * - 图片最长边超过 6000 像素时，按比例压缩到 6000 像素内
 */
export const prepareFileForUpload = async (file: File): Promise<File> => {
  if (!isBrowser || !file.type.startsWith('image/') || file.type === 'image/svg+xml') {
    return file;
  }

  const image = await loadUploadImage(file);
  const sourceWidth = image.naturalWidth;
  const sourceHeight = image.naturalHeight;
  const longestEdge = Math.max(sourceWidth, sourceHeight);

  if (!sourceWidth || !sourceHeight || longestEdge <= MAX_UPLOAD_IMAGE_EDGE) {
    return file;
  }

  const scale = MAX_UPLOAD_IMAGE_EDGE / longestEdge;
  const targetWidth = Math.max(1, Math.round(sourceWidth * scale));
  const targetHeight = Math.max(1, Math.round(sourceHeight * scale));
  const canvas = document.createElement('canvas');
  canvas.width = targetWidth;
  canvas.height = targetHeight;
  const ctx = canvas.getContext('2d');
  if (!ctx) {
    throw new Error('图片压缩失败');
  }

  ctx.drawImage(image, 0, 0, targetWidth, targetHeight);
  const requestedType = getImageResizeOutputType(file);
  const blob = await canvasToBlob(canvas, requestedType);
  const outputType = blob.type || requestedType;
  const outputName = outputType === file.type ? file.name : withImageExtension(file.name, outputType);

  return new File([blob], outputName, {
    type: outputType,
    lastModified: Date.now(),
  });
};

/**
 * 同步函数：将原始文件 key/url 转换为可直接使用的 /api/files/{key} 代理 URL
 * 后端代理模式下浏览器自动利用 Cache-Control + ETag 缓存，无需 JS 侧缓存
 */
export interface GetFileUrlOptions {
  redirect?: boolean;
}

export const getFileUrl = (raw?: string | null, options?: GetFileUrlOptions): string => {
  if (!raw) return '';
  const { key, externalUrl } = normalizeFileKey(raw);
  if (!key) return externalUrl && isValidMediaUrl(externalUrl) ? externalUrl : '';
  const base = `${API_BASE_URL}/api/files/${key}`;
  if (options?.redirect) {
    return `${base}?redirect=true`;
  }
  return base;
};

export const fileApi = {
  upload: async (file: File, visibility: 'public' | 'private' = 'private') => {
    const uploadFile = await prepareFileForUpload(file);
    const formData = new FormData();
    formData.append('file', uploadFile);
    formData.append('visibility', visibility);
    return request<FileUploadResponse>('/api/files', {
      method: 'POST',
      body: formData,
    }).then(res => ({ ...res, url: ensureHttpsUrl(res.url) }));
  },

  uploadOriginalText: (file: File) => {
    const formData = new FormData();
    formData.append('file', file);
    return request<OriginalTextUploadResponse>('/api/files/original-text', {
      method: 'POST',
      body: formData,
    }).then(res => ({ ...res, url: ensureHttpsUrl(res.url) }));
  },

  // 带进度回调的上传（适合大文件）
  uploadWithProgress: async (
    file: File,
    visibility: 'public' | 'private' = 'private',
    onProgress?: (percent: number) => void
  ): Promise<FileUploadResponse> => {
    onProgress?.(0);
    const uploadFile = await prepareFileForUpload(file);
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', uploadFile);
      formData.append('visibility', visibility);

      xhr.upload.onprogress = (event) => {
        if (event.lengthComputable && onProgress) {
          const percent = Math.round((event.loaded / event.total) * 100);
          onProgress(percent);
        }
      };

      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            const parsed = JSON.parse(xhr.responseText) as FileUploadResponse;
            resolve({ ...parsed, url: ensureHttpsUrl(parsed.url) });
          } catch {
            reject(new ApiError(xhr.status, 'Invalid JSON response'));
          }
        } else {
          // 携带 token 的上传收到 401 = 会话已失效，广播统一登出
          if (xhr.status === 401) notifyAuthExpired();
          let message = `Upload failed with status ${xhr.status}`;
          try {
            const errorData = JSON.parse(xhr.responseText);
            message = errorData.error || message;
          } catch {
            // ignore parse error
          }
          reject(new ApiError(xhr.status, message));
        }
      };

      xhr.onerror = () => reject(new ApiError(0, 'Network error'));

      xhr.open('POST', `${API_BASE_URL}/api/files`);

      const token = authStorage.getToken();
      if (token) {
        xhr.setRequestHeader('Authorization', `Bearer ${token}`);
      }

      xhr.send(formData);
    });
  },

};

// 分镜（帧集）API
export interface SceneFrameSetListResponse {
  total: number;
  data: SceneFrameSet[];
}

export interface SceneFrameSetVersionListResponse {
  total: number;
  data: SceneFrameSetVersion[];
}

export interface CreateFrameSetPayload {
  name: string;
  index: number;
}

export interface UpdateFrameSetPayload {
  name?: string;
  index?: number;
}

export const storyboardApi = {
  list: (sceneId: number) => request<SceneFrameSetListResponse>(`/api/scenes/${sceneId}/frame-sets`),
  create: (sceneId: number, payload: CreateFrameSetPayload) =>
    request<SceneFrameSet>(`/api/scenes/${sceneId}/frame-sets`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (sceneId: number, frameSetId: number, payload: UpdateFrameSetPayload) =>
    request<SceneFrameSet>(`/api/scenes/${sceneId}/frame-sets/${frameSetId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (sceneId: number, frameSetId: number) =>
    request(`/api/scenes/${sceneId}/frame-sets/${frameSetId}`, {
      method: 'DELETE',
    }),
  updateStartFrame: (sceneId: number, frameSetId: number, imageUrl: string) =>
    request<SceneFrameSetVersion>(`/api/scenes/${sceneId}/frame-sets/${frameSetId}/start-frame`, {
      method: 'PUT',
      body: JSON.stringify({ imageUrl }),
    }),
  updateEndFrame: (sceneId: number, frameSetId: number, imageUrl: string) =>
    request<SceneFrameSetVersion>(`/api/scenes/${sceneId}/frame-sets/${frameSetId}/end-frame`, {
      method: 'PUT',
      body: JSON.stringify({ imageUrl }),
    }),
  listStartVersions: (sceneId: number, frameSetId: number) =>
    request<SceneFrameSetVersionListResponse>(
      `/api/scenes/${sceneId}/frame-sets/${frameSetId}/start-frame/versions`
    ),
  listEndVersions: (sceneId: number, frameSetId: number) =>
    request<SceneFrameSetVersionListResponse>(
      `/api/scenes/${sceneId}/frame-sets/${frameSetId}/end-frame/versions`
    ),
  revertStartFrame: (sceneId: number, frameSetId: number, version: number) =>
    request<SceneFrameSet>(`/api/scenes/${sceneId}/frame-sets/${frameSetId}/start-frame/revert/${version}`, {
      method: 'PUT',
    }),
  revertEndFrame: (sceneId: number, frameSetId: number, version: number) =>
    request<SceneFrameSet>(`/api/scenes/${sceneId}/frame-sets/${frameSetId}/end-frame/revert/${version}`, {
      method: 'PUT',
    }),
};

// 动画制作 API（多动画）
export interface SceneAnimationListResponse {
  total: number;
  data: SceneAnimation[];
}

export interface SceneAnimationVersionListResponse {
  total: number;
  data: SceneAnimationVersion[];
}

export interface SceneAnimationGenerationTaskListResponse {
  total: number;
  data: SceneAnimationGenerationTask[];
}

export interface CreateAnimationPayload {
  name: string;
  index: number;
}

export interface UpdateAnimationPayload {
  name?: string;
  index?: number;
}

export interface GenerateAnimationPayload {
  text: string;
  ratio: '16:9' | '9:16';
  duration: number;
  model: 'doubao-seedance-2-0-260128' | 'doubao-seedance-2-0-fast-260128' | 'wan3.0-video' | 'wan3.0-video-prime';
  referenceImageKeys?: string[];
  referenceAudioKeys?: string[];
  referenceVideoKeys?: string[];
  appendArtStyle?: boolean;
}

export interface OptimizeAnimationPromptPayload {
  text: string;
  model?: string;
  /** 目标视频生成模型：wan3.0-* 万相系列按万相 3.0 官方提示词指南整理为五段结构化请求，其余按电影分镜范式优化 */
  videoModel?: string;
  referenceImageKeys?: string[];
  referenceAudioKeys?: string[];
  referenceVideoKeys?: string[];
}

export interface OptimizeAnimationPromptResponse {
  prompt: string;
  model: string;
}

export interface GenerateAnimationPromptDraftPayload {
  model?: string;
  /** 合并分镜数量（含当前分镜），1-10；超出本章剩余分镜数时按实际数量合并 */
  count?: number;
}

export interface AnimationPromptDraftCharacterReference {
  characterId: number;
  name: string;
  coreFeatures?: string;
  /** 人物真实出现在本次分镜画面中（自动附加人物参考图） */
  appears?: boolean;
  /** 人物在本次分镜有台词/对白/旁白/画外音（自动附加音色音频） */
  speaking?: boolean;
  /** 默认槽位人物参考图（后端按 三视图→半身正面→全身正面→侧视→后视 顺序取第一张已配置的） */
  imageKey?: string;
  /** 对应前端 Character 图像字段名（referenceImageUrl 等） */
  imageSlot?: string;
  imageSlotLabel?: string;
  /** 人物音色音频（仅在人物于本次分镜有对白时填充） */
  voiceAudioKey?: string;
}

export interface GenerateAnimationPromptDraftResponse {
  prompt: string;
  model: string;
  /** 实际合并的分镜数量（章节剩余分镜不足时小于请求值） */
  sceneCount: number;
  /** 本场出场/说话人物参考（出场→参考图、说话→音色，独立判定；来自大纲人设），前端据此自动附加 @人物 提及 */
  characterReferences?: AnimationPromptDraftCharacterReference[];
}

export const animationApi = {
  list: (sceneId: number) => request<SceneAnimationListResponse>(`/api/scenes/${sceneId}/animations`),
  create: (sceneId: number, payload: CreateAnimationPayload) =>
    request<SceneAnimation>(`/api/scenes/${sceneId}/animations`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (sceneId: number, animationId: number, payload: UpdateAnimationPayload) =>
    request<SceneAnimation>(`/api/scenes/${sceneId}/animations/${animationId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (sceneId: number, animationId: number) =>
    request(`/api/scenes/${sceneId}/animations/${animationId}`, {
      method: 'DELETE',
    }),
  upload: (sceneId: number, animationId: number, videoUrl: string) =>
    request<SceneAnimationVersion>(`/api/scenes/${sceneId}/animations/${animationId}/upload`, {
      method: 'PUT',
      body: JSON.stringify({ videoUrl }),
    }),
  listGenerationTasks: (sceneId: number, animationId: number) =>
    request<SceneAnimationGenerationTaskListResponse>(
      `/api/scenes/${sceneId}/animations/${animationId}/generation-tasks`
    ),
  createGenerationTask: (sceneId: number, animationId: number, payload: GenerateAnimationPayload) =>
    request<SceneAnimationGenerationTask>(`/api/scenes/${sceneId}/animations/${animationId}/generation-tasks`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  optimizePrompt: (sceneId: number, payload: OptimizeAnimationPromptPayload) =>
    request<OptimizeAnimationPromptResponse>(`/api/scenes/${sceneId}/animation-prompt/optimize`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  generatePromptDraft: (sceneId: number, payload: GenerateAnimationPromptDraftPayload) =>
    request<GenerateAnimationPromptDraftResponse>(`/api/scenes/${sceneId}/animation-prompt/draft`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  getGenerationTask: (sceneId: number, animationId: number, taskId: number) =>
    request<SceneAnimationGenerationTask>(
      `/api/scenes/${sceneId}/animations/${animationId}/generation-tasks/${taskId}`
    ),
  pollGenerationTask: (sceneId: number, animationId: number, taskId: number) =>
    request<SceneAnimationGenerationTask>(
      `/api/scenes/${sceneId}/animations/${animationId}/generation-tasks/${taskId}/poll`,
      {
        method: 'POST',
      }
    ),
  listVersions: (sceneId: number, animationId: number) =>
    request<SceneAnimationVersionListResponse>(`/api/scenes/${sceneId}/animations/${animationId}/versions`),
  revert: (sceneId: number, animationId: number, version: number) =>
    request<SceneAnimation>(`/api/scenes/${sceneId}/animations/${animationId}/revert/${version}`, {
      method: 'PUT',
    }),
  deleteVersion: (sceneId: number, animationId: number, version: number) =>
    request(`/api/scenes/${sceneId}/animations/${animationId}/versions/${version}`, {
      method: 'DELETE',
    }),
};

// 音频后期 API（多音轨）
export interface SceneAudio {
  id: number;
  sceneId: number;
  role: string;
  index: number;
  audioUrl?: string;
  audioVersion?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface AudioVersion {
  id: number;
  sceneAudioId: number;
  audioUrl: string;
  version: number;
  generationParams?: {
    textPrompt: string;
    referenceAudios?: Array<{ key: string; name: string }>;
    pitchRate?: number;
    speechRate?: number;
    loudnessRate?: number;
  };
  createdBy: number;
  createdAt: string;
}

export interface AudioVersionListResponse {
  total: number;
  data: AudioVersion[];
}

export interface SceneAudioListResponse {
  total: number;
  data: SceneAudio[];
}

export interface GenerateSceneAudioPayload {
  textPrompt: string;
  referenceAudioKeys?: string[];
  pitchRate?: number;
  speechRate?: number;
  loudnessRate?: number;
}

export const audioApi = {
  list: (sceneId: number) => request<SceneAudioListResponse>(`/api/scenes/${sceneId}/audios`),
  create: (sceneId: number, payload: { role: string; index: number }) =>
    request<SceneAudio>(`/api/scenes/${sceneId}/audios`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (sceneId: number, audioId: number, payload: { role?: string; index?: number }) =>
    request<SceneAudio>(`/api/scenes/${sceneId}/audios/${audioId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (sceneId: number, audioId: number) =>
    request(`/api/scenes/${sceneId}/audios/${audioId}`, {
      method: 'DELETE',
    }),
  upload: (sceneId: number, audioId: number, audioUrl: string) =>
    request<AudioVersion>(`/api/scenes/${sceneId}/audios/${audioId}/upload`, {
      method: 'PUT',
      body: JSON.stringify({ audioUrl }),
    }),
  generate: (sceneId: number, audioId: number, payload: GenerateSceneAudioPayload) =>
    request<AudioVersion>(`/api/scenes/${sceneId}/audios/${audioId}/generate`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  listVersions: (sceneId: number, audioId: number) =>
    request<AudioVersionListResponse>(`/api/scenes/${sceneId}/audios/${audioId}/versions`),
  revert: (sceneId: number, audioId: number, version: number) =>
    request<SceneAudio>(`/api/scenes/${sceneId}/audios/${audioId}/revert/${version}`, {
      method: 'PUT',
    }),
};

// 章节交付视频 API
export interface ChapterVideoResponse extends ChapterVideo {
  versionCount: number;
}

export interface ChapterVideoVersionListResponse {
  total: number;
  data: ChapterVideoVersion[];
}

export interface UploadChapterVideoPayload {
  videoUrl: string;
  previewUrl?: string;
  duration?: number;
  fileSize?: number;
  previewSize?: number;
  width?: number;
  height?: number;
  format?: string;
  codec?: string;
  bitrate?: number;
  previewBitrate?: number;
  remark?: string;
}

export interface UploadPreviewPayload {
  previewUrl: string;
  previewSize?: number;
  previewBitrate?: number;
}

export interface UpdateVideoStatusPayload {
  status: VideoStatus;
}

export const videoApi = {
  getInfo: (chapterId: number) => request<ChapterVideoResponse>(`/api/chapters/${chapterId}/video`),
  upload: (chapterId: number, payload: UploadChapterVideoPayload) =>
    request<ChapterVideoVersion>(`/api/chapters/${chapterId}/video`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  uploadPreview: (chapterId: number, payload: UploadPreviewPayload) =>
    request<ChapterVideo>(`/api/chapters/${chapterId}/video/preview`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  updateStatus: (chapterId: number, payload: UpdateVideoStatusPayload) =>
    request<ChapterVideo>(`/api/chapters/${chapterId}/video/status`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  uploadEditScript: (
    chapterId: number,
    file: File,
    onProgress?: (percent: number) => void
  ): Promise<ChapterVideo> => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', file);

      xhr.upload.onprogress = (event) => {
        if (event.lengthComputable && onProgress) {
          onProgress(Math.round((event.loaded / event.total) * 100));
        }
      };

      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            resolve(JSON.parse(xhr.responseText) as ChapterVideo);
          } catch {
            reject(new ApiError(xhr.status, 'Invalid JSON response'));
          }
          return;
        }
        // 携带 token 的上传收到 401 = 会话已失效，广播统一登出
        if (xhr.status === 401) notifyAuthExpired();
        let message = `Upload failed with status ${xhr.status}`;
        try {
          const errorData = JSON.parse(xhr.responseText);
          message = errorData.error || message;
        } catch {
          // ignore parse error
        }
        reject(new ApiError(xhr.status, message));
      };

      xhr.onerror = () => reject(new ApiError(0, 'Network error'));
      xhr.open('PUT', `${API_BASE_URL}/api/chapters/${chapterId}/video/edit-script`);

      const token = authStorage.getToken();
      if (token) xhr.setRequestHeader('Authorization', `Bearer ${token}`);

      xhr.send(formData);
    });
  },
  listVersions: (chapterId: number) =>
    request<ChapterVideoVersionListResponse>(`/api/chapters/${chapterId}/video/versions`),
  revert: (chapterId: number, version: number) =>
    request<ChapterVideo>(`/api/chapters/${chapterId}/video/revert/${version}`, {
      method: 'PUT',
    }),
  deleteVersion: (chapterId: number, version: number) =>
    request(`/api/chapters/${chapterId}/video/versions/${version}`, {
      method: 'DELETE',
    }),
  delete: (chapterId: number) =>
    request(`/api/chapters/${chapterId}/video`, {
      method: 'DELETE',
    }),
};

// 评论 API
export type SceneCommentModule = Extract<CommentModule, 'script' | 'storyboard' | 'animation' | 'audio'>;

export interface CreateCommentPayload {
  content: string;
  meta?: string;
}

export interface UpdateCommentPayload {
  content?: string;
  meta?: string;
}

export interface CommentCountsResponse {
  data: Record<number, number>;
  unresolvedCounts: Record<number, number>;
}

export const commentApi = {
  listScene: (sceneId: number, module: SceneCommentModule) =>
    request<CommentListResponse>(`/api/scenes/${sceneId}/comments?module=${module}`),
  createScene: (sceneId: number, module: SceneCommentModule, payload: CreateCommentPayload) =>
    request<Comment>(`/api/scenes/${sceneId}/comments?module=${module}`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  listChapter: (chapterId: number) => request<CommentListResponse>(`/api/chapters/${chapterId}/comments`),
  createChapter: (chapterId: number, payload: CreateCommentPayload) =>
    request<Comment>(`/api/chapters/${chapterId}/comments`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (id: number, payload: UpdateCommentPayload) =>
    request<Comment>(`/api/comments/${id}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (id: number) =>
    request(`/api/comments/${id}`, {
      method: 'DELETE',
    }),
  resolve: (id: number) =>
    request<Comment>(`/api/comments/${id}/resolve`, {
      method: 'PUT',
    }),
  unresolve: (id: number) =>
    request<Comment>(`/api/comments/${id}/unresolve`, {
      method: 'PUT',
    }),
  // 评论数统计（用于显示徽章）
  getSceneCommentCounts: (bookId: number, module: SceneCommentModule) =>
    request<CommentCountsResponse>(`/api/books/${bookId}/scenes/comment-counts?module=${module}`),
  getChapterCommentCounts: (bookId: number) =>
    request<CommentCountsResponse>(`/api/books/${bookId}/chapters/comment-counts`),
};

// 角色人设 API
export interface CharacterListResponse {
  total: number;
  data: Character[];
}

export interface CreateCharacterPayload {
  name: string;
  description?: string;
  coreFeatures?: string;
  referenceImageUrl?: string;
  halfBodyFrontImageUrl?: string;
  fullBodyFrontImageUrl?: string;
  fullBodySideImageUrl?: string;
  fullBodyBackImageUrl?: string;
  voiceAudioUrl?: string;
  index: number;
}

export interface UpdateCharacterPayload {
  name?: string;
  description?: string;
  coreFeatures?: string;
  referenceImageUrl?: string;
  halfBodyFrontImageUrl?: string;
  fullBodyFrontImageUrl?: string;
  fullBodySideImageUrl?: string;
  fullBodyBackImageUrl?: string;
  voiceAudioUrl?: string;
  index?: number;
}

export interface GenerateCharacterCoreFeaturesPayload {
  imageKey?: string;
  model?: string;
  name?: string;
  description?: string;
}

export interface GenerateCharacterCoreFeaturesResponse {
  coreFeatures: string;
  model: string;
}

export const characterApi = {
  list: (bookId: number) => request<CharacterListResponse>(`/api/books/${bookId}/characters`),
  create: (bookId: number, payload: CreateCharacterPayload) =>
    request<Character>(`/api/books/${bookId}/characters`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (bookId: number, characterId: number, payload: UpdateCharacterPayload) =>
    request<Character>(`/api/books/${bookId}/characters/${characterId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  generateCoreFeatures: (bookId: number, characterId: number, payload: GenerateCharacterCoreFeaturesPayload) =>
    request<GenerateCharacterCoreFeaturesResponse>(`/api/books/${bookId}/characters/${characterId}/core-features/generate`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  delete: (bookId: number, characterId: number) =>
    request(`/api/books/${bookId}/characters/${characterId}`, {
      method: 'DELETE',
    }),
};

// 场景资产 API
export interface SceneAssetListResponse {
  total: number;
  data: SceneAsset[];
}

export interface CreateSceneAssetPayload {
  name: string;
  code: string;
  description?: string;
  referenceImageUrls?: string[];
  index?: number;
}

export interface UpdateSceneAssetPayload {
  name?: string;
  code?: string;
  description?: string;
  referenceImageUrls?: string[];
  index?: number;
}

export const sceneAssetApi = {
  list: (bookId: number) =>
    request<SceneAssetListResponse>(`/api/books/${bookId}/scene-assets`),
  create: (bookId: number, payload: CreateSceneAssetPayload) =>
    request<SceneAsset>(`/api/books/${bookId}/scene-assets`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (bookId: number, sceneAssetId: number, payload: UpdateSceneAssetPayload) =>
    request<SceneAsset>(`/api/books/${bookId}/scene-assets/${sceneAssetId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (bookId: number, sceneAssetId: number) =>
    request(`/api/books/${bookId}/scene-assets/${sceneAssetId}`, {
      method: 'DELETE',
    }),
};

// 场景参考资料 API
export interface SceneReferenceListResponse {
  total: number;
  data: SceneReference[];
}

export interface CreateSceneReferencePayload {
  index: number;
  imageUrl?: string;
  description?: string;
}

export interface UpdateSceneReferencePayload {
  index?: number;
  imageUrl?: string;
  description?: string;
}

export const sceneReferenceApi = {
  list: (sceneId: number) =>
    request<SceneReferenceListResponse>(`/api/scenes/${sceneId}/references`),
  create: (sceneId: number, payload: CreateSceneReferencePayload) =>
    request<SceneReference>(`/api/scenes/${sceneId}/references`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  update: (sceneId: number, referenceId: number, payload: UpdateSceneReferencePayload) =>
    request<SceneReference>(`/api/scenes/${sceneId}/references/${referenceId}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  delete: (sceneId: number, referenceId: number) =>
    request(`/api/scenes/${sceneId}/references/${referenceId}`, {
      method: 'DELETE',
    }),
  deleteAll: (sceneId: number) =>
    request(`/api/scenes/${sceneId}/references`, {
      method: 'DELETE',
    }),
};

// LoRA 库 API
export const loraApi = {
  // 获取 LoRA 列表
  list: async (params?: LoraListParams): Promise<LoraListResponse> => {
    const searchParams = new URLSearchParams();
    if (params?.page) searchParams.set('page', String(params.page));
    if (params?.size) searchParams.set('size', String(params.size));
    if (params?.modelType) searchParams.set('modelType', params.modelType);
    if (params?.tag) searchParams.set('tag', params.tag);
    if (params?.keyword) searchParams.set('keyword', params.keyword);

    const queryString = searchParams.toString();
    const endpoint = `/api/loras${queryString ? `?${queryString}` : ''}`;

    return request<LoraListResponse>(endpoint);
  },

  // 创建 LoRA
  create: async (data: CreateLoraRequest): Promise<Lora> => {
    return request<Lora>('/api/loras', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },

  // 获取单个 LoRA 详情
  getById: async (id: number): Promise<Lora> => {
    return request<Lora>(`/api/loras/${id}`);
  },

  // 更新 LoRA
  update: async (id: number, data: UpdateLoraRequest): Promise<Lora> => {
    return request<Lora>(`/api/loras/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  },

  // 删除 LoRA
  delete: async (id: number): Promise<void> => {
    return request(`/api/loras/${id}`, {
      method: 'DELETE',
    });
  },

  // 获取所有标签
  getTags: async (): Promise<{ tags: string[] }> => {
    return request<{ tags: string[] }>('/api/loras/tags');
  },
};

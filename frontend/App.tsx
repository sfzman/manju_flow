
import React, { useState, useMemo, useEffect, useCallback, Suspense, lazy } from 'react';
import { ProductionStage, Project, Episode } from './types';
import { STAGE_CONFIG, STATUS_MAP, STATUS_FILTERS, DEFAULT_STATUS_FILTER_KEY } from './constants';
import { ImportBookModal } from './components/ImportBookModal';
import { LoraLibrary } from './components/LoraLibrary';
import { authApi, authStorage, bookApi, chapterApi, booksToProjects, CreateBookRequest, AuthResponse, isUnauthorizedError } from './api';
import { AuthPage } from './components/AuthPage';
import {
  Search,
  ChevronRight,
  Activity,
  BookOpen,
  ArrowLeft,
  PlusCircle,
  MoreVertical,
  Play,
  Loader2,
  AlertCircle,
  RefreshCw,
  Pencil,
  Trash2,
  Layers,
  Archive,
  ArchiveRestore,
  Star,
} from 'lucide-react';

// 懒加载编辑器组件 - 按需加载减少首屏体积
const OutlineEditor = lazy(() => import('./components/OutlineEditor').then(m => ({ default: m.OutlineEditor })));
const ScriptEditor = lazy(() => import('./components/ScriptEditor').then(m => ({ default: m.ScriptEditor })));
const StoryboardEditor = lazy(() => import('./components/StoryboardEditor').then(m => ({ default: m.StoryboardEditor })));
const AnimationEditor = lazy(() => import('./components/AnimationEditor').then(m => ({ default: m.AnimationEditor })));
const AudioEditor = lazy(() => import('./components/AudioEditor').then(m => ({ default: m.AudioEditor })));
const DeliverReview = lazy(() => import('./components/DeliverReview').then(m => ({ default: m.DeliverReview })));

// 编辑器加载占位组件
const EditorLoading: React.FC = () => (
  <div className="flex items-center justify-center h-full bg-[#0f0f0f]">
    <div className="flex flex-col items-center gap-4">
      <Loader2 size={32} className="text-blue-500 animate-spin" />
      <span className="text-white/40 text-sm font-medium">加载编辑器...</span>
    </div>
  </div>
);

const BUILD_VERSION = __APP_VERSION__ || 'dev';
const BUILD_TIME = (() => {
  const parsed = new Date(__BUILD_TIME__);
  return Number.isNaN(parsed.getTime()) ? __BUILD_TIME__ : parsed.toLocaleString('zh-CN', { hour12: false });
})();

const App: React.FC = () => {
  const [viewMode, setViewMode] = useState<'DASHBOARD' | 'PRODUCTION'>('DASHBOARD');
  const [libraryType, setLibraryType] = useState<'BOOK' | 'LORA'>('BOOK');
  const [currentStage, setCurrentStage] = useState<ProductionStage>(ProductionStage.OUTLINE);
  const [selectedProject, setSelectedProject] = useState<Project | null>(null);
  const [currentUser, setCurrentUser] = useState<AuthResponse['user'] | null>(null);
  const [authToken, setAuthToken] = useState<string | null>(() => authStorage.getToken());

  // 跨模块共享的场景状态 - 确保模块切换时保持选中的章节和场景
  const [activeChapterId, setActiveChapterId] = useState<number | null>(null);
  const [activeSceneId, setActiveSceneId] = useState<number | null>(null);

  const [statusFilter, setStatusFilter] = useState<string>(DEFAULT_STATUS_FILTER_KEY);
  const [searchKeyword, setSearchKeyword] = useState('');
  const [debouncedKeyword, setDebouncedKeyword] = useState('');
  const [activeMenuId, setActiveMenuId] = useState<number | null>(null);
  const [deletingId, setDeletingId] = useState<number | null>(null);
  const [mutatingId, setMutatingId] = useState<number | null>(null);
  const [favoriteMutatingIds, setFavoriteMutatingIds] = useState<Set<number>>(new Set());
  const [isEditModalOpen, setIsEditModalOpen] = useState(false);
  const [editingBookId, setEditingBookId] = useState<number | null>(null);
  const [editingData, setEditingData] = useState<CreateBookRequest | null>(null);
  const [isEditLoading, setIsEditLoading] = useState(false);

  // 项目列表状态
  const [projects, setProjects] = useState<Project[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // 导入弹窗状态
  const [isImportModalOpen, setIsImportModalOpen] = useState(false);

  // 章节数据预加载状态
  const [isLoadingEpisodes, setIsLoadingEpisodes] = useState(false);

  const handleAuthSuccess = (auth: AuthResponse) => {
    setAuthToken(auth.token);
    setCurrentUser(auth.user);
  };

  // silent = 会话已确认失效（401），无需再通知服务端删除会话
  const handleLogout = (options?: { silent?: boolean }) => {
    const token = authStorage.getToken();
    authStorage.clear();
    setAuthToken(null);
    setCurrentUser(null);
    setSelectedProject(null);
    setViewMode('DASHBOARD');
    // 手动登出时尽力删除服务端会话；失败不影响本地登出
    if (!options?.silent && token) {
      authApi.logout(token).catch(() => {});
    }
  };

  // 防抖处理搜索关键词
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedKeyword(searchKeyword);
    }, 300);
    return () => clearTimeout(timer);
  }, [searchKeyword]);

  // 加载项目列表
  const loadProjects = useCallback(async () => {
    if (!authToken) return;
    setIsLoading(true);
    setError(null);
    try {
      const params: { keyword?: string; status?: string; favorite?: boolean; size?: number } = { size: 100 };
      if (debouncedKeyword.trim()) {
        params.keyword = debouncedKeyword.trim();
      }
      const statusOption = STATUS_FILTERS.find(f => f.key === statusFilter);
      if (statusOption?.statuses) {
        params.status = statusOption.statuses;
      }
      if (statusOption?.favorite) {
        params.favorite = true;
      }
      const response = await bookApi.list(params);
      setProjects(booksToProjects(response.data));
    } catch (err) {
      console.error('Failed to load projects:', err);
      setError(err instanceof Error ? err.message : '加载失败，请检查网络连接');
    } finally {
      setIsLoading(false);
    }
  }, [statusFilter, debouncedKeyword, authToken]);

  // 初始加载和筛选变化时重新加载
  useEffect(() => {
    if (authToken) {
      loadProjects();
    } else {
      setIsLoading(false);
    }
  }, [loadProjects, authToken]);

  // 如果有 token，获取当前用户信息；仅在 401（会话失效）时才登出，
  // 网络/服务端瞬时错误保留 token，避免误踢下线
  useEffect(() => {
    const fetchMe = async () => {
      if (!authToken) return;
      try {
        const user = await authApi.me();
        setCurrentUser(user);
      } catch (err) {
        if (isUnauthorizedError(err)) {
          handleLogout({ silent: true });
        } else {
          console.error('Failed to fetch user:', err);
        }
      }
    };
    fetchMe();
  }, [authToken]);

  // 任意携带 token 的请求收到 401（会话过期/被删除）时统一登出
  useEffect(() => {
    const onAuthExpired = () => handleLogout({ silent: true });
    window.addEventListener('auth:expired', onAuthExpired);
    return () => window.removeEventListener('auth:expired', onAuthExpired);
  }, []);

  // 监听点击关闭下拉菜单
  useEffect(() => {
    const handleClick = () => setActiveMenuId(null);
    document.addEventListener('click', handleClick);
    return () => document.removeEventListener('click', handleClick);
  }, []);

  // 创建新作品
  const handleCreateBook = async (data: CreateBookRequest) => {
    await bookApi.create(data);
    // 重新加载列表
    await loadProjects();
  };

  // 进入项目并预加载章节数据
  const enterProject = async (project: Project) => {
    // 先进入创作模式，显示加载状态
    setSelectedProject({ ...project, episodes: [] });
    setViewMode('PRODUCTION');
    setCurrentStage(ProductionStage.OUTLINE);
    // 重置跨模块的场景状态
    setActiveChapterId(null);
    setActiveSceneId(null);

    // 预加载章节和场景数据
    setIsLoadingEpisodes(true);
    try {
      const res = await chapterApi.list(project.id, true);
      const episodes: Episode[] = (res.data || []).map(ch => ({
        id: ch.id,
        title: ch.title,
        index: ch.index,
        synopsis: ch.synopsis || '',
        status: ch.status as Episode['status'],
        scenes: (ch.scenes || [])
          .map(s => ({
            id: s.id,
            chapterId: s.chapterId ?? ch.id,
            index: s.index,
            description: s.description || '',
            cameraMovement: s.cameraMovement || '',
            dialogue: s.dialogue || '',
            transitionEffect: s.transitionEffect || '',
            status: s.status as Episode['status'],
            sceneAssetCode: s.sceneAssetCode ?? null,
            comments: [],
            references: s.references,
            thumbnailUrl: s.thumbnailUrl,
            frameSets: s.frameSets,
            animations: s.animations,
            audios: s.audios,
            createdAt: s.createdAt,
            updatedAt: s.updatedAt,
          }))
          .sort((a, b) => a.index - b.index),
      }));
      setSelectedProject(prev => prev ? { ...prev, episodes } : prev);
    } catch (err) {
      console.error('Failed to preload chapters:', err);
    } finally {
      setIsLoadingEpisodes(false);
    }
  };

  const handleEpisodesChange = (episodes: Episode[]) => {
    setSelectedProject(prev => (prev ? { ...prev, episodes } : prev));
    setProjects(prev => prev.map(p => (p.id === selectedProject?.id ? { ...p, episodes } : p)));
  };

  // 跨模块场景状态变更回调
  const handleActiveChapterChange = useCallback((chapterId: number | null) => {
    setActiveChapterId(chapterId);
  }, []);

  const handleActiveSceneChange = useCallback((sceneId: number | null) => {
    setActiveSceneId(sceneId);
  }, []);

  const handleDeleteBook = async (projectId: number) => {
    if (!window.confirm('确定删除该作品吗？此操作不可撤销。')) return;
    setDeletingId(projectId);
    try {
      await bookApi.delete(projectId);
      await loadProjects();
    } catch (err) {
      console.error('Failed to delete project:', err);
      alert('删除失败，请稍后再试');
    } finally {
      setDeletingId(null);
    }
  };

  const handleArchive = async (projectId: number) => {
    setMutatingId(projectId);
    try {
      await bookApi.archive(projectId);
      await loadProjects();
    } catch (err) {
      console.error('Failed to archive project:', err);
      alert('归档失败，请稍后再试');
    } finally {
      setMutatingId(null);
    }
  };

  const handleUnarchive = async (projectId: number) => {
    setMutatingId(projectId);
    try {
      await bookApi.unarchive(projectId);
      await loadProjects();
    } catch (err) {
      console.error('Failed to unarchive project:', err);
      alert('取消归档失败，请稍后再试');
    } finally {
      setMutatingId(null);
    }
  };

  const handleToggleFavorite = async (project: Project) => {
    if (favoriteMutatingIds.has(project.id)) return;

    const nextFavorite = !project.isFavorite;
    setFavoriteMutatingIds(prev => new Set(prev).add(project.id));
    setProjects(prev => prev.map(item => (
      item.id === project.id ? { ...item, isFavorite: nextFavorite } : item
    )));

    try {
      if (nextFavorite) {
        await bookApi.favorite(project.id);
      } else {
        await bookApi.unfavorite(project.id);
      }
    } catch (err) {
      console.error('Failed to update favorite:', err);
      setProjects(prev => prev.map(item => (
        item.id === project.id ? { ...item, isFavorite: project.isFavorite } : item
      )));
      alert(nextFavorite ? '收藏失败，请稍后再试' : '取消收藏失败，请稍后再试');
    } finally {
      setFavoriteMutatingIds(prev => {
        const next = new Set(prev);
        next.delete(project.id);
        return next;
      });
    }
  };

  const handleEditBook = async (project: Project) => {
    setEditingBookId(project.id);
    setIsEditModalOpen(true);
    setIsEditLoading(true);
    try {
      const book = await bookApi.getById(project.id);
      setEditingData({
        title: book.title,
        author: book.author,
        cover: book.cover || '',
        description: book.description || '',
        originalTextKey: book.originalTextKey || '',
        originalTextPreview: book.originalTextPreview || '',
      });
    } catch (err) {
      console.error('Failed to load book detail:', err);
      setEditingData({
        title: project.title,
        author: project.author,
        cover: project.cover,
        description: '',
        originalTextKey: project.originalTextKey || '',
        originalTextPreview: project.originalTextPreview || '',
      });
    } finally {
      setIsEditLoading(false);
    }
  };

  const handleUpdateBook = async (data: CreateBookRequest) => {
    if (!editingBookId) return;
    await bookApi.update(editingBookId, data);
    await loadProjects();
  };

  const closeEditModal = () => {
    setIsEditModalOpen(false);
    setEditingBookId(null);
    setEditingData(null);
    setIsEditLoading(false);
  };

  if (!authToken || !currentUser) {
    return <AuthPage onSuccess={handleAuthSuccess} />;
  }

  const filteredProjects = statusFilter === 'FAVORITES'
    ? projects.filter(project => project.isFavorite)
    : projects;

  const renderDashboard = () => {
    // LoRA 库模式
    if (libraryType === 'LORA') {
      return (
        <div className="flex flex-col h-full bg-[#0a0a0a]">
          {/* LoRA 库顶栏 */}
          <nav className="h-16 border-b border-white/5 bg-[#0f0f0f] px-8 flex items-center justify-between sticky top-0 z-30">
            <div className="flex items-center gap-10">
              <div className="flex items-center gap-2">
                <div className="w-8 h-8 bg-blue-600 rounded flex items-center justify-center text-white font-black text-xl italic shadow-lg shadow-blue-900/40">M</div>
                <h1 className="font-bold text-white tracking-tighter text-lg">ManjuFlow</h1>
              </div>
              {/* 主 Tab 切换 */}
              <div className="flex items-center gap-1 bg-white/5 rounded-lg p-1">
                <button
                  onClick={() => setLibraryType('BOOK')}
                  className="flex items-center gap-2 px-4 py-1.5 rounded-md text-xs font-bold transition-all text-white/40 hover:text-white"
                >
                  <BookOpen size={14} />
                  作品库
                </button>
                <button
                  className="flex items-center gap-2 px-4 py-1.5 rounded-md text-xs font-bold transition-all bg-purple-600 text-white shadow-lg"
                >
                  <Layers size={14} />
                  LoRA库
                </button>
              </div>
            </div>

            <div className="flex items-center gap-6">
              <div className="hidden md:flex items-center gap-2 px-3 py-2 rounded-lg bg-white/5 border border-white/10 text-white/70 text-sm">
                <div className="w-7 h-7 rounded-full bg-purple-600 text-white flex items-center justify-center font-bold uppercase">
                  {(currentUser?.nickname || currentUser?.username || '?')[0]}
                </div>
                <div>
                  <div className="font-semibold leading-tight">{currentUser?.nickname || currentUser?.username}</div>
                  <div className="text-white/40 text-xs">{currentUser?.username}</div>
                </div>
              </div>
              <button
                onClick={() => handleLogout()}
                className="text-white/50 text-sm hover:text-white underline underline-offset-4"
              >
                退出登录
              </button>
            </div>
          </nav>

          {/* LoRA 库内容 */}
          <div className="flex-1 overflow-hidden">
            <LoraLibrary />
          </div>

          <footer className="border-t border-white/5 bg-[#0f0f0f] text-white/40 text-[11px] text-center py-3">
            前端版本 {BUILD_VERSION} · 构建 {BUILD_TIME}
          </footer>
        </div>
      );
    }

    // 作品库模式
    return (
    <div className="flex flex-col h-full bg-[#0a0a0a]">
      {/* 媒体库顶栏 */}
      <nav className="h-16 border-b border-white/5 bg-[#0f0f0f] px-8 flex items-center justify-between sticky top-0 z-30">
        <div className="flex items-center gap-10">
          <div className="flex items-center gap-2">
            <div className="w-8 h-8 bg-blue-600 rounded flex items-center justify-center text-white font-black text-xl italic shadow-lg shadow-blue-900/40">M</div>
            <h1 className="font-bold text-white tracking-tighter text-lg">ManjuFlow</h1>
          </div>
          {/* 主 Tab 切换 */}
          <div className="flex items-center gap-1 bg-white/5 rounded-lg p-1">
            <button
              className="flex items-center gap-2 px-4 py-1.5 rounded-md text-xs font-bold transition-all bg-blue-600 text-white shadow-lg"
            >
              <BookOpen size={14} />
              作品库
            </button>
            <button
              onClick={() => setLibraryType('LORA')}
              className="flex items-center gap-2 px-4 py-1.5 rounded-md text-xs font-bold transition-all text-white/40 hover:text-white"
            >
              <Layers size={14} />
              LoRA库
            </button>
          </div>
        </div>

        <div className="flex items-center gap-6">
          <div className="hidden md:flex items-center gap-2 px-3 py-2 rounded-lg bg-white/5 border border-white/10 text-white/70 text-sm">
            <div className="w-7 h-7 rounded-full bg-blue-600 text-white flex items-center justify-center font-bold uppercase">
              {(currentUser.nickname || currentUser.username || '?')[0]}
            </div>
            <div>
              <div className="font-semibold leading-tight">{currentUser.nickname || currentUser.username}</div>
              <div className="text-white/40 text-xs">{currentUser.username}</div>
            </div>
          </div>
          <button
            onClick={() => handleLogout()}
            className="text-white/50 text-sm hover:text-white underline underline-offset-4"
          >
            退出登录
          </button>
        </div>
      </nav>

      {/* 媒体库内容区 */}
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-7xl mx-auto">
          {/* 作品库筛选栏 */}
          <div className="sticky top-0 z-20 bg-[#0a0a0a] px-4 md:px-12 py-4 border-b border-white/5">
            <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-3">
              <div className="flex items-center gap-1 bg-white/5 rounded-lg p-1 overflow-x-auto max-w-full">
                {STATUS_FILTERS.map(f => (
                  <button
                    key={f.key}
                    onClick={() => setStatusFilter(f.key)}
                    aria-pressed={statusFilter === f.key}
                    className={`shrink-0 px-4 py-1.5 rounded-md text-xs font-bold transition-all ${statusFilter === f.key ? 'bg-blue-600 text-white shadow-lg' : 'text-white/40 hover:text-white'}`}
                  >
                    <span className="flex items-center gap-1.5">
                      {f.favorite && <Star size={12} fill="currentColor" />}
                      {f.label}
                    </span>
                  </button>
                ))}
              </div>
              <div className="flex items-center gap-3 md:gap-4">
                <div className="relative flex-1 lg:flex-none">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 text-white/20" size={14} />
                  <input
                    className="bg-white/5 border border-white/10 rounded-full py-2 pl-9 pr-4 text-sm text-white focus:outline-none focus:ring-1 focus:ring-blue-500 w-full lg:w-56"
                    placeholder="搜索书名、作者..."
                    value={searchKeyword}
                    onChange={(e) => setSearchKeyword(e.target.value)}
                  />
                </div>
                <button
                  onClick={() => setIsImportModalOpen(true)}
                  className="shrink-0 flex items-center gap-2 px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-bold hover:bg-blue-500 transition-all"
                >
                  <PlusCircle size={16} /> 导入新作品
                </button>
              </div>
            </div>
          </div>

          {/* 内容区域 */}
          <div className="p-4 md:p-12">
            <header className="mb-10">
              <h2 className="text-3xl font-bold text-white mb-2">欢迎回来</h2>
              <p className="text-white/30 text-sm">
                {isLoading ? '正在加载...' : `共有 ${filteredProjects.length} 个作品`}
              </p>
            </header>

          {/* 加载状态 */}
          {isLoading && (
            <div className="flex flex-col items-center justify-center py-20">
              <Loader2 size={40} className="text-blue-500 animate-spin mb-4" />
              <p className="text-white/40 text-sm">正在加载作品列表...</p>
            </div>
          )}

          {/* 错误状态 */}
          {!isLoading && error && (
            <div className="flex flex-col items-center justify-center py-20">
              <AlertCircle size={40} className="text-red-400 mb-4" />
              <p className="text-red-400 text-sm mb-4">{error}</p>
              <button
                onClick={loadProjects}
                className="flex items-center gap-2 px-4 py-2 bg-white/5 border border-white/10 text-white rounded-lg text-sm hover:bg-white/10 transition-all"
              >
                <RefreshCw size={16} /> 重新加载
              </button>
            </div>
          )}

          {/* 空状态 */}
          {!isLoading && !error && filteredProjects.length === 0 && (
            <div className="flex flex-col items-center justify-center py-20">
              <BookOpen size={40} className="text-white/20 mb-4" />
              <p className="text-white/40 text-sm mb-4">
                {searchKeyword
                  ? '没有找到匹配的作品'
                  : statusFilter === 'FAVORITES'
                  ? '还没有收藏作品，点击卡片右上角的星星即可收藏'
                  : '暂无作品，点击上方按钮导入'}
              </p>
              {!searchKeyword && statusFilter !== 'FAVORITES' && (
                <button
                  onClick={() => setIsImportModalOpen(true)}
                  className="flex items-center gap-2 px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-bold hover:bg-blue-500 transition-all"
                >
                  <PlusCircle size={16} /> 导入新作品
                </button>
              )}
            </div>
          )}

          {/* 作品列表 */}
          {!isLoading && !error && filteredProjects.length > 0 && (
            <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-4 md:gap-10">
              {filteredProjects.map(p => (
                <div
                  key={p.id}
                  onClick={() => enterProject(p)}
                  className="group relative flex flex-col cursor-pointer"
                >
                  <div className="aspect-[2/3] rounded-2xl overflow-hidden border border-white/10 bg-zinc-900 relative shadow-2xl transition-all duration-500 group-hover:-translate-y-2 group-hover:border-blue-500/50">
                    <img src={p.cover} className="w-full h-full object-cover transition-transform duration-700 group-hover:scale-110" alt={p.title} />
                    <button
                      type="button"
                      aria-pressed={p.isFavorite}
                      aria-label={p.isFavorite ? `取消收藏${p.title}` : `收藏${p.title}`}
                      title={p.isFavorite ? '取消收藏' : '收藏'}
                      disabled={favoriteMutatingIds.has(p.id)}
                      onClick={(e) => {
                        e.stopPropagation();
                        handleToggleFavorite(p);
                      }}
                      className={`absolute top-4 right-4 z-10 w-9 h-9 rounded-full border backdrop-blur-md flex items-center justify-center transition-all disabled:cursor-wait ${
                        p.isFavorite
                          ? 'bg-amber-400 border-amber-300 text-zinc-950 shadow-lg shadow-amber-950/30'
                          : 'bg-black/55 border-white/20 text-white/70 hover:text-amber-300 hover:border-amber-300/60 hover:bg-black/75'
                      } ${favoriteMutatingIds.has(p.id) ? 'animate-pulse' : ''}`}
                    >
                      <Star size={17} fill={p.isFavorite ? 'currentColor' : 'none'} />
                    </button>
                    <div className="absolute inset-0 bg-gradient-to-t from-black/90 via-black/20 to-transparent opacity-0 group-hover:opacity-100 transition-opacity flex flex-col justify-end p-6">
                      <div className="flex items-center gap-3">
                        <div className="flex-1 h-12 bg-white text-black rounded-xl flex items-center justify-center font-bold text-sm tracking-widest gap-2">
                          <Play size={16} fill="currentColor" /> 进入制作
                        </div>
                        <div className="relative">
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation();
                              setActiveMenuId(activeMenuId === p.id ? null : p.id);
                            }}
                            className="w-12 h-12 bg-white/10 backdrop-blur-md rounded-xl flex items-center justify-center text-white border border-white/20 hover:bg-white/20"
                          >
                            <MoreVertical size={20} />
                          </button>
                          {activeMenuId === p.id && (
                            <div
                              onClick={(e) => e.stopPropagation()}
                              className="absolute right-0 bottom-14 w-40 bg-[#101010] border border-white/10 rounded-xl shadow-2xl py-1 z-30"
                            >
                              <button
                                className="w-full px-3 py-2 text-left text-sm text-white/80 hover:bg-white/5 flex items-center gap-2 transition-colors"
                                onClick={() => {
                                  setActiveMenuId(null);
                                  handleEditBook(p);
                                }}
                              >
                                <Pencil size={16} />
                                <span>编辑</span>
                              </button>
                              <button
                                className="w-full px-3 py-2 text-left text-sm text-white/80 hover:bg-white/5 flex items-center gap-2 transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
                                disabled={mutatingId === p.id}
                                onClick={() => {
                                  setActiveMenuId(null);
                                  if (p.productionStatus === 'ARCHIVED') {
                                    handleUnarchive(p.id);
                                  } else {
                                    handleArchive(p.id);
                                  }
                                }}
                              >
                                {mutatingId === p.id ? (
                                  <Loader2 size={16} className="animate-spin" />
                                ) : p.productionStatus === 'ARCHIVED' ? (
                                  <ArchiveRestore size={16} />
                                ) : (
                                  <Archive size={16} />
                                )}
                                <span>
                                  {mutatingId === p.id
                                    ? '处理中...'
                                    : p.productionStatus === 'ARCHIVED'
                                    ? '取消归档'
                                    : '归档'}
                                </span>
                              </button>
                              <button
                                className="w-full px-3 py-2 text-left text-sm text-red-400 hover:bg-red-500/10 flex items-center gap-2 transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
                                disabled={deletingId === p.id}
                                onClick={() => {
                                  setActiveMenuId(null);
                                  handleDeleteBook(p.id);
                                }}
                              >
                                {deletingId === p.id ? (
                                  <Loader2 size={16} className="animate-spin" />
                                ) : (
                                  <Trash2 size={16} />
                                )}
                                <span>{deletingId === p.id ? '删除中...' : '删除'}</span>
                              </button>
                            </div>
                          )}
                        </div>
                      </div>
                    </div>
                    {/* 状态标 */}
                    <div className="absolute top-4 left-4 flex flex-col gap-2">
                      <span className={`px-2 py-0.5 rounded-md border text-[9px] font-black uppercase tracking-widest ${
                        p.productionStatus === 'IN_PROGRESS' ? 'bg-blue-600/40 border-blue-500 text-blue-100' :
                        p.productionStatus === 'COMPLETED' ? 'bg-green-600/40 border-green-500 text-green-100' :
                        p.productionStatus === 'ARCHIVED' ? 'bg-zinc-700/50 border-zinc-600 text-zinc-300' :
                        'bg-zinc-600/40 border-zinc-500 text-zinc-100'
                      }`}>
                        {STATUS_MAP[p.productionStatus]}
                      </span>
                    </div>
                  </div>
                  <div className="mt-4 px-1">
                    <h4 className="text-white font-bold text-lg group-hover:text-blue-400 transition-colors truncate">{p.title}</h4>
                    <p className="text-white/30 text-xs font-medium uppercase tracking-wider">{p.author}</p>
                  </div>
                </div>
              ))}
            </div>
          )}
          </div>
        </div>
      </div>

      <footer className="border-t border-white/5 bg-[#0f0f0f] text-white/40 text-[11px] text-center py-3">
        前端版本 {BUILD_VERSION} · 构建 {BUILD_TIME}
      </footer>

      {/* 导入作品弹窗 */}
      <ImportBookModal
        isOpen={isImportModalOpen}
        onClose={() => setIsImportModalOpen(false)}
        onSubmit={handleCreateBook}
      />
      <ImportBookModal
        isOpen={isEditModalOpen}
        onClose={closeEditModal}
        onSubmit={handleUpdateBook}
        initialData={editingData || undefined}
        mode="edit"
        isLoading={isEditLoading}
      />
    </div>
    );
  };

  const renderProduction = () => {
    if (!selectedProject) return null;

    const renderContent = () => {
      switch (currentStage) {
        case ProductionStage.OUTLINE:
          return (
            <OutlineEditor
              bookId={selectedProject.id}
              initialOutline={selectedProject.outline}
              initialCharacters={selectedProject.characters}
            />
          );
        case ProductionStage.SCRIPT:
          return (
            <ScriptEditor
              bookId={selectedProject.id}
              episodes={selectedProject.episodes}
              onEpisodesChange={handleEpisodesChange}
              initialChapterId={activeChapterId}
              initialSceneId={activeSceneId}
              onActiveChapterChange={handleActiveChapterChange}
              onActiveSceneChange={handleActiveSceneChange}
            />
          );
        case ProductionStage.ART:
          if (isLoadingEpisodes) {
            return <EditorLoading />;
          }
          return selectedProject.episodes.length > 0 ? (
            <StoryboardEditor
              bookId={selectedProject.id}
              episodes={selectedProject.episodes}
              initialChapterId={activeChapterId}
              initialSceneId={activeSceneId}
              onActiveChapterChange={handleActiveChapterChange}
              onActiveSceneChange={handleActiveSceneChange}
            />
          ) : (
            <div className="p-20 text-center text-white/20">暂无剧本</div>
          );
        case ProductionStage.ANIMATE:
          if (isLoadingEpisodes) {
            return <EditorLoading />;
          }
          return selectedProject.episodes.length > 0 ? (
            <AnimationEditor
              bookId={selectedProject.id}
              episodes={selectedProject.episodes}
              initialChapterId={activeChapterId}
              initialSceneId={activeSceneId}
              onActiveChapterChange={handleActiveChapterChange}
              onActiveSceneChange={handleActiveSceneChange}
            />
          ) : (
            <div className="p-20 text-center text-white/20">暂无分镜</div>
          );
        case ProductionStage.AUDIO:
          if (isLoadingEpisodes) {
            return <EditorLoading />;
          }
          return selectedProject.episodes.length > 0 ? (
            <AudioEditor
              bookId={selectedProject.id}
              episodes={selectedProject.episodes}
              initialChapterId={activeChapterId}
              initialSceneId={activeSceneId}
              onActiveChapterChange={handleActiveChapterChange}
              onActiveSceneChange={handleActiveSceneChange}
            />
          ) : (
            <div className="p-20 text-center text-white/20">暂无动画</div>
          );
        case ProductionStage.REVIEW:
          if (isLoadingEpisodes) {
            return <EditorLoading />;
          }
          return (
            <DeliverReview
              episodes={selectedProject.episodes}
              bookId={selectedProject.id}
            />
          );
        default:
          return null;
      }
    };

    return (
      <div className="flex flex-col h-full overflow-hidden">
        {/* 生产区顶栏 */}
        <nav className="h-14 bg-[#1a1a1a] border-b border-white/10 flex items-center justify-between px-6 z-20 shadow-xl">
          <div className="flex items-center gap-6">
            <button 
              onClick={() => setViewMode('DASHBOARD')}
              className="p-2 hover:bg-white/5 rounded-lg text-white/40 hover:text-white transition-all flex items-center gap-2 group"
            >
              <ArrowLeft size={18} className="group-hover:-translate-x-1 transition-transform" />
              <span className="text-xs font-bold uppercase tracking-widest">返回书库</span>
            </button>
            <div className="h-6 w-[1px] bg-white/10" />
            <div className="flex flex-col">
              <div className="flex items-center gap-2 text-xs font-semibold text-white/60">
                <span className="text-white">{selectedProject.title}</span>
                <ChevronRight size={14} className="opacity-30" />
                <span className="text-blue-400">第一话 制作中</span>
              </div>
            </div>
          </div>

          <div className="flex items-center gap-4">
            <div className="flex items-center gap-3 px-3 py-1.5 bg-white/5 rounded-full border border-white/10">
               <Activity size={14} className="text-green-500" />
               <span className="text-[10px] font-bold text-white/60 uppercase tracking-widest">云端实时同步</span>
            </div>
            <div className="w-8 h-8 rounded-full bg-zinc-800 border border-white/10 flex items-center justify-center overflow-hidden">
              <img src="https://picsum.photos/seed/avatar/40" alt="user" />
            </div>
          </div>
        </nav>

        {/* 模块主视图 */}
        <div className="flex-1 overflow-hidden">
          <Suspense fallback={<EditorLoading />}>
            {renderContent()}
          </Suspense>
        </div>

        {/* DaVinci 风格底部导航 */}
        <footer className="h-20 bg-[#161616] border-t border-white/10 flex items-center justify-center z-20 shadow-[0_-10px_20px_rgba(0,0,0,0.5)]">
          <div className="flex items-center gap-1 bg-black/40 p-1 rounded-xl border border-white/5 backdrop-blur-md">
            {STAGE_CONFIG.map(({ stage, label, icon }) => (
              <button
                key={stage}
                onClick={() => setCurrentStage(stage)}
                className={`flex flex-col items-center justify-center w-24 h-14 rounded-lg transition-all relative group ${
                  currentStage === stage ? 'text-white' : 'text-white/40 hover:text-white/80 hover:bg-white/5'
                }`}
              >
                <div className={`mb-1 transition-transform group-hover:scale-110 ${currentStage === stage ? 'text-blue-500 drop-shadow-[0_0_8px_rgba(59,130,246,0.5)]' : ''}`}>
                  {icon}
                </div>
                <span className="text-[10px] font-bold uppercase tracking-widest">{label}</span>
                {currentStage === stage && (
                  <div className="absolute -top-1 left-1/2 -translate-x-1/2 w-1.5 h-1.5 rounded-full bg-blue-500 shadow-[0_0_10px_#3b82f6]" />
                )}
              </button>
            ))}
          </div>
        </footer>
      </div>
    );
  };

  return (
    <div className="h-screen w-screen overflow-hidden text-white selection:bg-blue-500/30">
      {viewMode === 'DASHBOARD' ? renderDashboard() : renderProduction()}
    </div>
  );
};

export default App;

import React from 'react';
import { History, Info, Loader2, MonitorPlay, Trash2 } from 'lucide-react';

/** 历史版本条目公共字段（各模块版本对象的最小约束） */
export interface VersionHistoryEntry {
  id: number;
  version: number;
  createdAt?: string;
}

interface VersionHistoryMenuProps<T extends VersionHistoryEntry> {
  /** 版本列表（新版本在前） */
  versions: T[];
  /** 当前生效版本号，用于高亮与禁删 */
  currentVersion?: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 传入则显示「预览」按钮 */
  onPreview?: (version: T) => void;
  /** 正在预览的版本号，用于「预览中」态 */
  previewVersion?: number | null;
  /** 返回 false 时预览按钮禁用 */
  canPreview?: (version: T) => boolean;
  onRevert: (version: T) => void;
  revertDisabled?: boolean;
  /** 传入则显示「删除」按钮（软删除） */
  onDelete?: (version: T) => void;
  /** 正在删除的版本号，用于按钮加载态 */
  deletingVersion?: number | null;
  /** 版本号旁的自定义徽标（如「含预览」） */
  renderVersionBadge?: (version: T) => React.ReactNode;
  /** 版本条目内的补充信息（如文件大小） */
  renderVersionMeta?: (version: T) => React.ReactNode;
}

const REVERT_HINT = '将预览中的版本设为交付版本（默认最新）';

/**
 * 历史版本下拉菜单
 * 动画制作 / 审核交付模块共用：每个版本提供「预览 / 设为当前 / 删除（软删除）」操作
 */
export function VersionHistoryMenu<T extends VersionHistoryEntry>({
  versions,
  currentVersion,
  open,
  onOpenChange,
  onPreview,
  previewVersion,
  canPreview,
  onRevert,
  revertDisabled = false,
  onDelete,
  deletingVersion = null,
  renderVersionBadge,
  renderVersionMeta,
}: VersionHistoryMenuProps<T>) {
  const disabled = versions.length === 0;

  return (
    <div className="relative">
      <button
        disabled={disabled}
        onClick={() => onOpenChange(!open)}
        className={`flex items-center gap-1 text-[11px] px-3 py-1.5 rounded-lg border border-white/10 transition-all ${
          disabled
            ? 'bg-white/5 text-white/30 cursor-not-allowed'
            : 'bg-white/5 hover:bg-white/10 text-white/70'
        }`}
      >
        <History size={12} /> 历史版本
      </button>
      {open && versions.length > 0 && (
        <>
          <div
            className="fixed inset-0 z-10"
            onClick={() => onOpenChange(false)}
          />
          <div className="absolute right-0 mt-2 w-80 bg-[#0f0f0f] border border-white/10 rounded-xl shadow-2xl z-20 overflow-hidden">
            <div className="px-3 py-2 border-b border-white/10 text-white/60 text-[11px]">
              <span>共 {versions.length} 个版本</span>
            </div>
            <div className="max-h-80 overflow-y-auto divide-y divide-white/10">
              {versions.map(version => {
                const time = version.createdAt
                  ? new Date(version.createdAt).toLocaleString('zh-CN', { hour12: false })
                  : '';
                const isCurrent = currentVersion === version.version;
                const isPreviewing = previewVersion === version.version;
                const previewDisabled = canPreview ? !canPreview(version) : false;
                const isDeleting = deletingVersion === version.version;
                return (
                  <div
                    key={version.id}
                    className={`p-3 space-y-1 ${isCurrent ? 'bg-blue-600/10' : 'bg-transparent hover:bg-white/5'}`}
                  >
                    <div className="flex items-center justify-between text-white/80">
                      <div>
                        <p className="text-sm font-semibold flex items-center gap-2">
                          版本 #{version.version}
                          {isCurrent && (
                            <span className="text-[10px] px-2 py-0.5 rounded-full bg-blue-500/15 text-blue-200 border border-blue-500/30">
                              当前
                            </span>
                          )}
                          {renderVersionBadge?.(version)}
                        </p>
                        <div className="text-[11px] text-white/40">{time || '时间未知'}</div>
                      </div>
                      <div className="text-[10px] text-white/40">ID: {version.id}</div>
                    </div>
                    {renderVersionMeta?.(version)}
                    <div className="flex items-center justify-between gap-2">
                      {onPreview ? (
                        <button
                          onClick={() => onPreview(version)}
                          disabled={previewDisabled}
                          title={previewDisabled ? '该版本缺少视频链接' : '预览该版本'}
                          className={`text-[11px] px-2 py-1 rounded-lg border flex items-center gap-1 transition-all ${
                            isPreviewing
                              ? 'bg-blue-600/20 border-blue-500/50 text-white'
                              : 'bg-white/10 hover:bg-white/20 border-white/10 text-white/80 disabled:opacity-40 disabled:cursor-not-allowed'
                          }`}
                        >
                          <MonitorPlay size={12} />
                          {isPreviewing ? '预览中' : '预览'}
                        </button>
                      ) : (
                        <span />
                      )}
                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => onRevert(version)}
                          disabled={revertDisabled}
                          className="text-[11px] px-2 py-1 rounded-lg bg-blue-600 hover:bg-blue-500 text-white border border-blue-500/50 disabled:opacity-60 disabled:cursor-not-allowed"
                        >
                          设为当前
                        </button>
                        {onDelete && (
                          <button
                            onClick={() => onDelete(version)}
                            disabled={isCurrent || isDeleting}
                            title={isCurrent ? '当前版本不可删除，请先切换到其他版本' : '删除该历史版本'}
                            className="text-[11px] p-1.5 rounded-lg bg-white/5 border border-white/10 text-red-300 hover:bg-red-500/20 hover:border-red-500/40 transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
                          >
                            {isDeleting ? (
                              <Loader2 size={12} className="animate-spin" />
                            ) : (
                              <Trash2 size={12} />
                            )}
                          </button>
                        )}
                        <div className="relative group">
                          <Info size={14} className="text-white/30" />
                          <div className="absolute right-0 top-6 w-48 p-2 rounded-md bg-black/80 border border-white/10 text-[11px] text-white/70 opacity-0 group-hover:opacity-100 pointer-events-none transition-opacity">
                            {REVERT_HINT}
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

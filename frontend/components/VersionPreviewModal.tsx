import React, { useEffect, useRef } from 'react';
import { MonitorPlay, X } from 'lucide-react';

interface VersionPreviewModalProps {
  /** 可直接播放的视频地址（已通过 getFileUrl 解析） */
  url?: string;
  /** 预览的版本号，用于标题展示 */
  version?: number;
  /** 自定义标题，默认「版本 #N 预览」 */
  title?: string;
  onClose: () => void;
}

/**
 * 历史版本视频预览弹窗
 * 动画制作 / 审核交付模块的历史版本列表共用，点击「预览」后全屏播放指定版本
 */
export const VersionPreviewModal: React.FC<VersionPreviewModalProps> = ({ url, version, title, onClose }) => {
  const videoRef = useRef<HTMLVideoElement>(null);

  // 卸载时（包括关闭、切换预览版本）停止播放并复位进度
  useEffect(() => {
    return () => {
      if (videoRef.current) {
        videoRef.current.pause();
        videoRef.current.currentTime = 0;
      }
    };
  }, []);

  // Esc 关闭预览
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center px-4">
      <div className="absolute inset-0 bg-black/70" onClick={onClose} />
      <div className="relative w-full max-w-4xl bg-[#0b0b0b] border border-white/10 rounded-2xl shadow-2xl overflow-hidden">
        <div className="flex items-center justify-between px-4 py-3 border-b border-white/10 text-white">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <MonitorPlay size={16} className="text-blue-400" />
            <span>{title || `版本 #${version ?? '—'} 预览`}</span>
          </div>
          <button
            onClick={onClose}
            className="text-white/60 hover:text-white rounded-full p-1 hover:bg-white/10 transition-colors"
            title="关闭预览 (Esc)"
          >
            <X size={16} />
          </button>
        </div>
        <div className="aspect-video bg-black">
          {url ? (
            <video
              ref={videoRef}
              src={url}
              className="w-full h-full object-contain"
              controls
              autoPlay
            />
          ) : (
            <div className="w-full h-full flex items-center justify-center text-white/50 text-sm">无预览链接</div>
          )}
        </div>
      </div>
    </div>
  );
};

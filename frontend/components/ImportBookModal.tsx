import React, { useEffect, useRef, useState } from 'react';
import { X, Upload, Loader2, FileText } from 'lucide-react';
import { CreateBookRequest, ensureHttpsUrl, fileApi } from '../api';
import { Toast, useToast } from './Toast';

interface ImportBookModalProps {
  isOpen: boolean;
  onClose: () => void;
  onSubmit: (data: CreateBookRequest) => Promise<void>;
  initialData?: CreateBookRequest;
  mode?: 'create' | 'edit';
  isLoading?: boolean;
}

type CoverField = 'cover' | 'coverLandscape';

interface CoverUploadBlockProps {
  /** 区块标题，如「封面图片（竖版 9:16）」 */
  label: string;
  /** 按钮文案短名，如「封面」「横版封面」 */
  name: string;
  hint: string;
  value: string;
  previewBoxClassName: string;
  isUploading: boolean;
  onSelectFile: (file: File | null | undefined) => void;
  onRemove: () => void;
}

// 封面上传块（竖版/横版通用）：预览、选择文件、拖拽上传
const CoverUploadBlock: React.FC<CoverUploadBlockProps> = ({
  label,
  name,
  hint,
  value,
  previewBoxClassName,
  isUploading,
  onSelectFile,
  onRemove,
}) => {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [isDragOver, setIsDragOver] = useState(false);

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (fileInputRef.current) {
      fileInputRef.current.value = '';
    }
    onSelectFile(file);
  };

  const handleDrop = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragOver(false);
    if (isUploading) return;
    onSelectFile(e.dataTransfer.files?.[0]);
  };

  return (
    <div>
      <label className="block text-xs font-bold text-white/40 uppercase tracking-widest mb-2">
        {label}
      </label>
      <div
        className={`flex items-start gap-4 p-4 rounded-xl border bg-gradient-to-r from-white/5 via-white/5 to-blue-900/10 transition-colors ${
          isDragOver ? 'border-blue-500/60 bg-blue-900/20' : 'border-white/10'
        }`}
        onDragOver={e => {
          e.preventDefault();
          e.dataTransfer.dropEffect = 'copy';
          setIsDragOver(true);
        }}
        onDragLeave={() => setIsDragOver(false)}
        onDrop={handleDrop}
      >
        <div className={`${previewBoxClassName} rounded-lg overflow-hidden border border-white/10 bg-white/5 relative flex items-center justify-center text-white/30 text-[11px]`}>
          {value ? (
            <>
              <img src={value} alt={`${name}预览`} className="w-full h-full object-cover" />
              <span className="absolute left-2 top-2 px-2 py-1 rounded-md bg-black/50 text-[10px] font-bold uppercase tracking-widest text-white/70">
                Preview
              </span>
            </>
          ) : (
            '暂无封面'
          )}
        </div>
        <div className="flex-1 space-y-3">
          <div className="flex flex-wrap gap-2">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/*"
              className="hidden"
              onChange={handleFileChange}
            />
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={isUploading}
              className="px-4 py-2 rounded-lg bg-blue-600 text-white font-semibold hover:bg-blue-500 transition-all disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2 shadow-lg shadow-blue-900/30"
            >
              {isUploading ? (
                <>
                  <Loader2 size={16} className="animate-spin" />
                  <span>上传中...</span>
                </>
              ) : (
                <>
                  <Upload size={16} />
                  <span>上传{name}</span>
                </>
              )}
            </button>
            {value && (
              <button
                type="button"
                onClick={onRemove}
                className="px-4 py-2 rounded-lg border border-white/10 text-white/70 font-semibold hover:bg-white/10 transition-all"
              >
                移除{name}
              </button>
            )}
          </div>
          <p className="text-white/40 text-xs leading-relaxed">
            {hint}
          </p>
        </div>
      </div>
    </div>
  );
};

export const ImportBookModal: React.FC<ImportBookModalProps> = ({
  isOpen,
  onClose,
  onSubmit,
  initialData,
  mode = 'create',
  isLoading = false,
}) => {
  const defaultFormData: CreateBookRequest = {
    title: '',
    author: '',
    cover: '',
    coverLandscape: '',
    description: '',
    originalTextKey: '',
    originalTextPreview: '',
  };
  const [formData, setFormData] = useState<CreateBookRequest>(initialData || defaultFormData);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [isUploadingCover, setIsUploadingCover] = useState(false);
  const [isUploadingCoverLandscape, setIsUploadingCoverLandscape] = useState(false);
  const [isUploadingOriginalText, setIsUploadingOriginalText] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isOriginalDragOver, setIsOriginalDragOver] = useState(false);
  const [autoSaveQueued, setAutoSaveQueued] = useState(false);
  const originalTextInputRef = useRef<HTMLInputElement>(null);
  const { toast, showToast, hideToast } = useToast();

  const isEditMode = mode === 'edit';

  useEffect(() => {
    if (isOpen) {
      const hydrated = initialData
        ? {
            ...initialData,
            cover: ensureHttpsUrl(initialData.cover),
            coverLandscape: ensureHttpsUrl(initialData.coverLandscape),
          }
        : defaultFormData;
      setFormData(hydrated);
      setError(null);
      hideToast();
    }
  }, [isOpen, initialData]);

  // 弹窗关闭期间到达的上传完成事件不应触发自动保存
  useEffect(() => {
    if (!isOpen && autoSaveQueued) {
      setAutoSaveQueued(false);
    }
  }, [isOpen, autoSaveQueued]);

  // 编辑模式：上传成功后自动保存一次，避免用户上传后未点保存就关闭弹窗导致改动丢失。
  // 多个文件并发上传时等全部完成合并为一次保存；创建模式尚无作品记录，不自动保存。
  useEffect(() => {
    if (!isOpen || !isEditMode || !autoSaveQueued) return;
    if (isUploadingCover || isUploadingCoverLandscape || isUploadingOriginalText) return;
    setAutoSaveQueued(false);
    // 表单不完整时不自动保存，由手动提交统一校验提示
    if (!formData.title.trim() || !formData.author.trim()) return;

    // formData 取触发当次渲染的值，已包含刚上传成功的封面/原文
    const data = formData;
    const autoSave = async () => {
      try {
        await onSubmit(data);
        showToast('已自动保存', 'success');
      } catch (err) {
        console.error('Failed to auto save book', err);
        setError(
          err instanceof Error && err.message
            ? `自动保存失败：${err.message}，请点击「保存修改」重试`
            : '自动保存失败，请点击「保存修改」重试'
        );
      }
    };
    autoSave();
  }, [isOpen, isEditMode, autoSaveQueued, isUploadingCover, isUploadingCoverLandscape, isUploadingOriginalText]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!formData.title.trim()) {
      setError('请输入作品名称');
      return;
    }
    if (!formData.author.trim()) {
      setError('请输入作者名称');
      return;
    }
    if (isUploadingCover || isUploadingCoverLandscape || isUploadingOriginalText) {
      setError('文件上传中，请稍候');
      return;
    }

    setIsSubmitting(true);
    try {
      await onSubmit(formData);
      // 重置表单
      setFormData(defaultFormData);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : '提交失败，请稍后重试');
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleInputChange = (
    e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>
  ) => {
    const { name, value } = e.target;
    setFormData(prev => ({ ...prev, [name]: value }));
  };

  const handleCoverUpload = async (field: CoverField, file?: File | null) => {
    if (!file) return;
    const wasUploading = field === 'cover' ? isUploadingCover : isUploadingCoverLandscape;
    if (wasUploading) return;
    if (field === 'cover') {
      setIsUploadingCover(true);
    } else {
      setIsUploadingCoverLandscape(true);
    }
    setError(null);
    try {
      const res = await fileApi.upload(file, 'public');
      setFormData(prev => ({ ...prev, [field]: ensureHttpsUrl(res.url) || res.key }));
      if (isEditMode) {
        setAutoSaveQueued(true); // 编辑模式上传成功后自动保存一次
      }
    } catch (err) {
      console.error('Failed to upload cover', err);
      const fallback = field === 'cover' ? '封面上传失败，请稍后重试' : '横版封面上传失败，请稍后重试';
      setError(err instanceof Error ? err.message : fallback);
    } finally {
      if (field === 'cover') {
        setIsUploadingCover(false);
      } else {
        setIsUploadingCoverLandscape(false);
      }
    }
  };

  const handleOriginalTextUpload = async (file?: File | null) => {
    if (!file) return;
    const isTxt = file.name.toLowerCase().endsWith('.txt');
    if (!isTxt) {
      setError('原文上传只支持 txt 文件');
      if (originalTextInputRef.current) {
        originalTextInputRef.current.value = '';
      }
      return;
    }

    setIsUploadingOriginalText(true);
    setError(null);
    try {
      const res = await fileApi.uploadOriginalText(file);
      setFormData(prev => ({
        ...prev,
        originalTextKey: res.key,
        originalTextPreview: res.preview,
      }));
      if (isEditMode) {
        setAutoSaveQueued(true); // 编辑模式上传成功后自动保存一次
      }
    } catch (err) {
      console.error('Failed to upload original text', err);
      setError(err instanceof Error ? err.message : '原文上传失败，请稍后重试');
    } finally {
      setIsUploadingOriginalText(false);
      setIsOriginalDragOver(false);
      if (originalTextInputRef.current) {
        originalTextInputRef.current.value = '';
      }
    }
  };

  const handleOriginalTextFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    handleOriginalTextUpload(file);
  };

  const handleOriginalTextDrop = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    if (isUploadingOriginalText) {
      setIsOriginalDragOver(false);
      return;
    }
    const file = e.dataTransfer.files?.[0];
    handleOriginalTextUpload(file);
    if (!file) {
      setIsOriginalDragOver(false);
    }
  };

  if (!isOpen) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* 背景遮罩 */}
      <div
        className="absolute inset-0 bg-black/80 backdrop-blur-sm"
        onClick={onClose}
      />

      {/* 弹窗内容 */}
      <div className="relative bg-[#1a1a1a] border border-white/10 rounded-2xl w-full max-w-lg mx-4 shadow-2xl max-h-[90vh] overflow-hidden flex flex-col">
        <Toast toast={toast} onClose={hideToast} />
        {/* 标题栏 */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-white/10">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-blue-600/20 flex items-center justify-center">
              <Upload size={20} className="text-blue-400" />
            </div>
            <h2 className="text-lg font-bold text-white">{isEditMode ? '编辑作品' : '导入新作品'}</h2>
          </div>
          <button
            onClick={onClose}
            className="p-2 hover:bg-white/5 rounded-lg text-white/40 hover:text-white transition-all"
          >
            <X size={20} />
          </button>
        </div>

        {/* 表单 */}
        {isLoading ? (
          <div className="p-6 flex items-center justify-center gap-3 text-white/60">
            <Loader2 size={20} className="animate-spin" />
            <span>加载作品信息...</span>
          </div>
        ) : (
          <form onSubmit={handleSubmit} className="p-6 space-y-5 overflow-y-auto">
            {error && (
              <div className="p-3 bg-red-500/10 border border-red-500/20 rounded-lg text-red-400 text-sm">
                {error}
              </div>
            )}

            {/* 作品名称 */}
            <div>
              <label className="block text-xs font-bold text-white/40 uppercase tracking-widest mb-2">
                作品名称 <span className="text-red-400">*</span>
              </label>
              <input
                type="text"
                name="title"
                value={formData.title}
                onChange={handleInputChange}
                placeholder="请输入作品名称"
                className="w-full bg-white/5 border border-white/10 rounded-xl py-3 px-4 text-white placeholder-white/20 focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500 transition-all"
              />
            </div>

            {/* 作者 */}
            <div>
              <label className="block text-xs font-bold text-white/40 uppercase tracking-widest mb-2">
                作者 <span className="text-red-400">*</span>
              </label>
              <input
                type="text"
                name="author"
                value={formData.author}
                onChange={handleInputChange}
                placeholder="请输入作者名称"
                className="w-full bg-white/5 border border-white/10 rounded-xl py-3 px-4 text-white placeholder-white/20 focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500 transition-all"
              />
            </div>

            {/* 封面（竖版） */}
            <CoverUploadBlock
              label="封面图片（竖版 9:16）"
              name="封面"
              hint="竖版封面，用于作品卡片展示。上传图片后会自动填充封面链接并保存到作品信息，支持拖拽到此区域。建议尺寸 600×900，JPG/PNG 均可。"
              value={formData.cover}
              previewBoxClassName="w-24 h-32"
              isUploading={isUploadingCover}
              onSelectFile={file => handleCoverUpload('cover', file)}
              onRemove={() => setFormData(prev => ({ ...prev, cover: '' }))}
            />

            {/* 封面（横版） */}
            <CoverUploadBlock
              label="横版封面（16:9）"
              name="横版封面"
              hint="横版封面，用于横屏场景展示（可选）。上传图片后自动保存到作品信息，支持拖拽到此区域。建议尺寸 1280×720，JPG/PNG 均可。"
              value={formData.coverLandscape}
              previewBoxClassName="w-32 h-24"
              isUploading={isUploadingCoverLandscape}
              onSelectFile={file => handleCoverUpload('coverLandscape', file)}
              onRemove={() => setFormData(prev => ({ ...prev, coverLandscape: '' }))}
            />

            {/* 简介 */}
            <div>
              <label className="block text-xs font-bold text-white/40 uppercase tracking-widest mb-2">
                作品简介
              </label>
              <textarea
                name="description"
                value={formData.description}
                onChange={handleInputChange}
                placeholder="请输入作品简介（可选）"
                rows={3}
                className="w-full bg-white/5 border border-white/10 rounded-xl py-3 px-4 text-white placeholder-white/20 focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500 transition-all resize-none"
              />
            </div>

            {/* 原文 */}
            <div>
              <label className="block text-xs font-bold text-white/40 uppercase tracking-widest mb-2">
                作品原文
              </label>
              <div
                className={`rounded-xl border bg-white/5 p-4 transition-colors ${
                  isOriginalDragOver ? 'border-blue-500/60 bg-blue-900/20' : 'border-white/10'
                }`}
                onDragOver={e => {
                  e.preventDefault();
                  e.dataTransfer.dropEffect = 'copy';
                  setIsOriginalDragOver(true);
                }}
                onDragLeave={() => setIsOriginalDragOver(false)}
                onDrop={handleOriginalTextDrop}
              >
                <div className="flex items-start gap-3">
                  <div className="mt-0.5 rounded-lg bg-blue-500/10 p-2 text-blue-300">
                    <FileText size={18} />
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex flex-wrap gap-2">
                      <input
                        ref={originalTextInputRef}
                        type="file"
                        accept=".txt,text/plain"
                        className="hidden"
                        onChange={handleOriginalTextFileChange}
                      />
                      <button
                        type="button"
                        onClick={() => originalTextInputRef.current?.click()}
                        disabled={isUploadingOriginalText}
                        className="px-4 py-2 rounded-lg bg-blue-600 text-white font-semibold hover:bg-blue-500 transition-all disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2 shadow-lg shadow-blue-900/30"
                      >
                        {isUploadingOriginalText ? (
                          <>
                            <Loader2 size={16} className="animate-spin" />
                            <span>上传中...</span>
                          </>
                        ) : (
                          <>
                            <Upload size={16} />
                            <span>{formData.originalTextKey ? '重新上传原文' : '上传 TXT 原文'}</span>
                          </>
                        )}
                      </button>
                      {formData.originalTextKey && (
                        <button
                          type="button"
                          onClick={() => setFormData(prev => ({ ...prev, originalTextKey: '', originalTextPreview: '' }))}
                          className="px-4 py-2 rounded-lg border border-white/10 text-white/70 font-semibold hover:bg-white/10 transition-all"
                        >
                          移除原文
                        </button>
                      )}
                    </div>
                    <p className="mt-3 text-white/40 text-xs leading-relaxed">
                      仅支持 .txt；上传后保存到 OSS，并自动截取前 800 字用于大纲人设顶部预览。
                    </p>
                    {formData.originalTextPreview && (
                      <div className="mt-3 rounded-lg border border-white/10 bg-black/20 p-3">
                        <div className="mb-1 text-[10px] font-bold uppercase tracking-widest text-white/30">预览</div>
                        <p className="line-clamp-3 whitespace-pre-wrap text-xs leading-relaxed text-white/65">
                          {formData.originalTextPreview}
                        </p>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            </div>

            {/* 提交按钮 */}
            <div className="flex gap-3 pt-2">
              <button
                type="button"
                onClick={onClose}
                className="flex-1 py-3 rounded-xl border border-white/10 text-white/60 font-semibold hover:bg-white/5 transition-all"
              >
                取消
              </button>
              <button
                type="submit"
                disabled={isSubmitting || isUploadingCover || isUploadingCoverLandscape || isUploadingOriginalText}
                className="flex-1 py-3 rounded-xl bg-blue-600 text-white font-semibold hover:bg-blue-500 transition-all disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2"
              >
                {isSubmitting ? (
                  <>
                    <Loader2 size={18} className="animate-spin" />
                    <span>{isEditMode ? '保存中...' : '创建中...'}</span>
                  </>
                ) : (
                  <span>{isEditMode ? '保存修改' : '确认导入'}</span>
                )}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
};

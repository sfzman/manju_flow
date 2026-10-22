
export enum ProductionStage {
  OUTLINE = 'OUTLINE',
  SCRIPT = 'SCRIPT',
  ART = 'ART',
  ANIMATE = 'ANIMATE',
  AUDIO = 'AUDIO',
  REVIEW = 'REVIEW'
}

export type Status = 'DRAFT' | 'IN_PROGRESS' | 'COMPLETED' | 'ARCHIVED';

export type CommentModule = 'script' | 'storyboard' | 'animation' | 'audio' | 'review';
export type CommentTargetType = 'scene' | 'chapter';
export type CommentStatus = 'unresolved' | 'resolved';

export interface CommentUser {
  id: number;
  username: string;
  nickname?: string;
}

export interface Comment {
  id: number;
  content: string;
  targetType: CommentTargetType;
  targetId: number;
  module: CommentModule;
  userId: number;
  user?: CommentUser;
  meta?: string;
  status: CommentStatus;
  createdAt: string;
  updatedAt: string;
}

export interface CommentListResponse {
  total: number;
  data: Comment[];
}

export interface ReviewCommentMeta {
  timecode?: string;
  seconds?: number;
}

// 通用评论元数据（支持图片附件）
export interface CommentMeta {
  // 审核交付时间点
  timecode?: string;
  seconds?: number;
  // 图片附件
  imageUrl?: string;
  imageName?: string;
}

// 场景参考资料（支持多个）
export interface SceneReference {
  id: number;
  sceneId: number;
  index: number;
  imageUrl?: string;
  description?: string;
  imageUploadedAt?: string;
  createdBy?: number;
  updatedBy?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface Scene {
  id: number;
  chapterId?: number;
  sceneAssetCode?: string | null;
  index: number;
  description: string;
  cameraMovement: string;
  dialogue: string;
  transitionEffect?: string; // 转场或剪辑手法
  status: Status;
  comments?: Comment[];
  references?: SceneReference[]; // 多参考资料（一对多）
  thumbnailUrl?: string; // 场景预览缩略图（首帧）
  audios?: SceneAudioTrack[]; // 多音轨
  frameSets?: SceneFrameSet[];
  animations?: SceneAnimation[];
  createdAt?: string;
  updatedAt?: string;
}

export interface SceneAudioTrack {
  id: number;
  sceneId: number;
  role: string;
  index: number;
  audioUrl?: string;
  audioVersion?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface SceneFrameSet {
  id: number;
  sceneId: number;
  name: string;
  index: number;
  startFrameUrl?: string;
  startFrameVersion?: number;
  endFrameUrl?: string;
  endFrameVersion?: number;
  createdAt?: string;
  updatedAt?: string;
}

export type FrameType = 'START' | 'END';

export interface SceneFrameSetVersion {
  id: number;
  sceneFrameSetId: number;
  frameType: FrameType;
  imageUrl: string;
  version: number;
  createdBy?: number;
  createdAt?: string;
}

export interface SceneAnimation {
  id: number;
  sceneId: number;
  name: string;
  index: number;
  animationUrl?: string;
  animationVersion?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface SceneAnimationVersion {
  id: number;
  sceneAnimationId: number;
  generationTaskId?: number;
  videoUrl: string;
  version: number;
  createdBy?: number;
  createdAt?: string;
}

export type AnimationTaskStatus = 'PENDING' | 'PROCESSING' | 'SUCCEEDED' | 'FAILED';

export interface AnimationTaskReferenceAsset {
  source: string;
  name: string;
  mimeType?: string;
}

export interface SceneAnimationGenerationTask {
  id: number;
  sceneId: number;
  sceneAnimationId: number;
  arkTaskId?: string;
  status: AnimationTaskStatus;
  text: string;
  appendArtStyle?: boolean;
  ratio: '16:9' | '9:16';
  duration: number;
  model: 'doubao-seedance-2-0-260128' | 'doubao-seedance-2-0-fast-260128' | 'wan3.0-video' | 'wan3.0-video-prime';
  referenceImageKeys?: string[];
  referenceAudioKeys?: string[];
  referenceVideoKeys?: string[];
  referenceImageAssets?: AnimationTaskReferenceAsset[];
  referenceAudioAssets?: AnimationTaskReferenceAsset[];
  referenceVideoAssets?: AnimationTaskReferenceAsset[];
  resultVideoUrl?: string;
  outputVersion?: number;
  errorMessage?: string;
  lastPolledAt?: string;
  completedAt?: string;
  createdBy?: number;
  createdAt?: string;
  updatedAt?: string;
}

export type VideoStatus = 'PENDING' | 'PROCESSING' | 'READY' | 'FAILED';

export interface ChapterVideo {
  id: number;
  chapterId: number;
  videoUrl?: string;
  previewUrl?: string;
  videoVersion?: number;
  status: VideoStatus;
  duration?: number;
  fileSize?: number;
  previewSize?: number;
  width?: number;
  height?: number;
  format?: string;
  codec?: string;
  bitrate?: number;
  previewBitrate?: number;
  editScriptUrl?: string;
  editScriptName?: string;
  editScriptSize?: number;
  editScriptUploadedAt?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface ChapterVideoVersion {
  id: number;
  chapterVideoId: number;
  videoUrl: string;
  previewUrl?: string;
  version: number;
  duration?: number;
  fileSize?: number;
  previewSize?: number;
  width?: number;
  height?: number;
  remark?: string;
  createdBy: number;
  createdAt: string;
}

export interface Episode {
  id: number;
  title: string;
  index?: number;
  synopsis?: string;
  status: Status;
  scenes: Scene[];
}

export interface Project {
  id: number;
  title: string;
  author: string;
  cover: string;
  coverLandscape?: string;
  productionStatus: Status;
  episodes: Episode[];
  assignedWriter?: string;
  assignedArtist?: string;
  assignedEditor?: string;
  outline?: string;
  originalTextKey?: string;
  originalTextPreview?: string;
  characters?: Character[];
  isFavorite: boolean;
}

export interface Character {
  id: number;
  bookId: number;
  name: string;
  description: string;
  coreFeatures?: string;
  referenceImageUrl?: string;
  halfBodyFrontImageUrl?: string;
  fullBodyFrontImageUrl?: string;
  fullBodySideImageUrl?: string;
  fullBodyBackImageUrl?: string;
  voiceAudioUrl?: string;
  index: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface SceneAsset {
  id: number;
  bookId: number;
  name: string;
  code: string;
  description: string;
  referenceImageUrls: string[];
  index: number;
  createdAt?: string;
  updatedAt?: string;
}

// LoRA 相关类型
export type LoraModelType = 'SD_1.5' | 'SDXL';

export interface Lora {
  id: number;
  name: string;
  description: string;
  modelType: LoraModelType;
  tags: string[];
  fileUrl: string;
  fileSize: number;
  previewUrl: string;
  configUrl: string;
  uploaderId: number;
  createdAt: string;
  updatedAt: string;
}

export interface CreateLoraRequest {
  name: string;
  description?: string;
  modelType: LoraModelType;
  tags?: string[];
  fileUrl?: string;
  fileSize?: number;
  previewUrl?: string;
  configUrl?: string;
}

export interface UpdateLoraRequest {
  name?: string;
  description?: string;
  modelType?: LoraModelType;
  tags?: string[];
  fileUrl?: string;
  fileSize?: number;
  previewUrl?: string;
  configUrl?: string;
}

export interface LoraListResponse {
  total: number;
  page: number;
  size: number;
  data: Lora[];
}

export interface LoraListParams {
  page?: number;
  size?: number;
  modelType?: LoraModelType;
  tag?: string;
  keyword?: string;
}

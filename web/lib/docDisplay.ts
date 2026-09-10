import { File, FileImage, FileText, FileType2 } from "lucide-react";
import type { LucideIcon } from "lucide-react";

export type FileTypeMeta = {
  label: string;
  icon: LucideIcon;
  iconClass: string;
  badgeClass: string;
};

const META: Record<string, FileTypeMeta> = {
  txt: { label: "TXT", icon: FileText, iconClass: "text-slate-500", badgeClass: "bg-slate-100 text-slate-600" },
  md: { label: "MD", icon: FileText, iconClass: "text-slate-500", badgeClass: "bg-slate-100 text-slate-600" },
  csv: { label: "CSV", icon: FileText, iconClass: "text-slate-500", badgeClass: "bg-slate-100 text-slate-600" },
  log: { label: "LOG", icon: FileText, iconClass: "text-slate-500", badgeClass: "bg-slate-100 text-slate-600" },
  docx: { label: "DOCX", icon: FileType2, iconClass: "text-blue-600", badgeClass: "bg-blue-50 text-blue-700" },
  doc: { label: "DOC", icon: FileType2, iconClass: "text-blue-600", badgeClass: "bg-blue-50 text-blue-700" },
  pdf: { label: "PDF", icon: FileText, iconClass: "text-red-600", badgeClass: "bg-red-50 text-red-700" },
  png: { label: "PNG", icon: FileImage, iconClass: "text-emerald-600", badgeClass: "bg-emerald-50 text-emerald-700" },
  jpg: { label: "JPG", icon: FileImage, iconClass: "text-emerald-600", badgeClass: "bg-emerald-50 text-emerald-700" },
  jpeg: { label: "JPEG", icon: FileImage, iconClass: "text-emerald-600", badgeClass: "bg-emerald-50 text-emerald-700" },
  webp: { label: "WEBP", icon: FileImage, iconClass: "text-emerald-600", badgeClass: "bg-emerald-50 text-emerald-700" },
  bmp: { label: "BMP", icon: FileImage, iconClass: "text-emerald-600", badgeClass: "bg-emerald-50 text-emerald-700" },
};

const FALLBACK: FileTypeMeta = {
  label: "FILE",
  icon: File,
  iconClass: "text-slate-400",
  badgeClass: "bg-slate-100 text-slate-500",
};

/** Infer file-type display meta from a file name's extension. */
export function getFileTypeMeta(fileName: string): FileTypeMeta {
  const ext = (fileName.split(".").pop() || "").toLowerCase();
  return META[ext] ?? FALLBACK;
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Render an uploader id nicely: demo users by name, real UUIDs truncated. */
export function formatUploader(id?: string): { label: string; title?: string } {
  if (!id) return { label: "—" };
  if (id === "demo-user") return { label: "演示用户" };
  if (id === "demo-admin") return { label: "演示管理员" };
  if (UUID_RE.test(id)) return { label: `${id.slice(0, 8)}…`, title: id };
  return { label: id };
}

/** Format ingestion stage milliseconds for document and upload views. */
export function formatDurationMs(ms?: number): string {
  if (ms == null || ms <= 0) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return `${minutes}m ${seconds}s`;
}

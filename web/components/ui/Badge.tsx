export type BadgeTone = "neutral" | "info" | "success" | "warning" | "danger" | "brand";

const tones: Record<BadgeTone, string> = {
  neutral: "bg-slate-100 text-slate-600",
  info: "bg-blue-50 text-blue-700",
  success: "bg-emerald-50 text-emerald-700",
  warning: "bg-amber-50 text-amber-700",
  danger: "bg-red-50 text-red-700",
  brand: "bg-indigo-50 text-indigo-700",
};

export function Badge({
  tone = "neutral",
  mono,
  children,
  className,
}: {
  tone?: BadgeTone;
  mono?: boolean;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span
      className={`inline-flex items-center rounded px-2 py-0.5 text-xs font-medium ${tones[tone]} ${mono ? "font-mono" : ""} ${className ?? ""}`}
    >
      {children}
    </span>
  );
}

// 收敛各页重复的 statusChip：completed→success / processing→info / failed→danger / 其余→warning
export function statusTone(status: string): BadgeTone {
  const s = status.toLowerCase();
  if (s === "completed") return "success";
  if (s === "processing") return "info";
  if (s === "failed") return "danger";
  return "warning";
}

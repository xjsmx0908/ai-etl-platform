import { Loader2 } from "lucide-react";

export function Spinner({ size = "md", label }: { size?: "sm" | "md" | "lg"; label?: string }) {
  const sizes = { sm: "h-3 w-3", md: "h-4 w-4", lg: "h-6 w-6" } as const;
  return (
    <div className="flex items-center justify-center gap-2 py-8 text-sm text-slate-400">
      <Loader2 className={`${sizes[size]} animate-spin`} />
      {label ?? "加载中…"}
    </div>
  );
}

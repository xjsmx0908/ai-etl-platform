export function Card({
  padding = "md",
  header,
  meta,
  footer,
  children,
  className,
}: React.HTMLAttributes<HTMLDivElement> & {
  padding?: "none" | "sm" | "md" | "lg";
  header?: React.ReactNode;
  meta?: React.ReactNode;
  footer?: React.ReactNode;
}) {
  const pads = { none: "", sm: "p-4", md: "p-5", lg: "p-6" } as const;
  return (
    <div className={`overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm ${className ?? ""}`}>
      {header && (
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <div className="text-sm font-semibold text-slate-500">{header}</div>
          {meta && <div className="text-xs text-slate-400">{meta}</div>}
        </div>
      )}
      <div className={pads[padding]}>{children}</div>
      {footer && <div className="border-t border-slate-100 px-4 py-3">{footer}</div>}
    </div>
  );
}

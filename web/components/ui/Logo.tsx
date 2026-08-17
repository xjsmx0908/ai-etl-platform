import Link from "next/link";

export function Logo({ size = "md", showWordmark = true }: { size?: "sm" | "md" | "lg"; showWordmark?: boolean }) {
  const box = { sm: "h-7 w-7", md: "h-8 w-8", lg: "h-10 w-10" } as const;
  const text = { sm: "text-sm", md: "text-base", lg: "text-lg" } as const;
  return (
    <Link href="/" className="flex items-center gap-2.5">
      <svg viewBox="0 0 32 32" className={box[size]}>
        <defs>
          <linearGradient id="brandLogo" x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
            <stop stopColor="#2563EB" />
            <stop offset="1" stopColor="#4F46E5" />
          </linearGradient>
        </defs>
        <rect width="32" height="32" rx="8" fill="url(#brandLogo)" />
        <circle cx="16" cy="16" r="5" fill="#FFFFFF" />
        <circle cx="9" cy="9.5" r="2.4" fill="rgba(255,255,255,0.72)" />
        <circle cx="23.5" cy="22" r="2.4" fill="rgba(255,255,255,0.72)" />
        <path d="M11.5 12.5 L14.5 15 M21.5 19.5 L17.5 17" stroke="#FFFFFF" strokeWidth="1.4" strokeLinecap="round" />
      </svg>
      {showWordmark && <span className={`font-semibold text-slate-900 ${text[size]}`}>知境</span>}
    </Link>
  );
}

import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "AI-ETL 企业知识库",
  description: "企业文档知识问答平台",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="zh-CN">
      <body className="min-h-screen bg-slate-50 text-slate-900">
        {children}
      </body>
    </html>
  );
}

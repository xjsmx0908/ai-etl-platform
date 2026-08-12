import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "AI ETL 知识问答",
  description: "企业文档知识库 RAG 演示",
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

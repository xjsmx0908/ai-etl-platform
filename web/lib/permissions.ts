import type { Role } from "./types";

export const READONLY_UPLOAD_DENIED_MESSAGE =
  "当前账号为只读用户，没有数据接入权限。如需上传文档，请联系管理员。";

export function canUploadDocuments(role: Role | null | undefined): boolean {
  return role === "admin" || role === "user";
}

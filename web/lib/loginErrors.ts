const LOGIN_ERROR_MESSAGES: Record<string, string> = {
  "invalid credentials": "用户名或密码错误",
  "invalid_credentials": "用户名或密码错误",
  "user is inactive": "账号已停用",
  "demo login is unavailable": "演示登录暂不可用",
  "account must be user or admin": "请选择演示账号",
};

export function localizeLoginError(message: string | undefined | null, status?: number): string {
  const raw = (message || "").trim();
  const mapped = LOGIN_ERROR_MESSAGES[raw.toLowerCase()];
  if (mapped) return mapped;
  if (raw && /[^\x00-\x7F]/.test(raw)) return raw;
  if (status === 401) return "用户名或密码错误";
  if (status === 403) return "账号已停用";
  if (status && status !== 200) return `登录失败: ${status}`;
  return raw || "登录失败";
}

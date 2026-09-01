# 个人演示身份运行时

## 范围

PD2 提供一个隔离的本机 Keycloak 26.3.3 运行时，用虚构用户验证已实现的 F1～F6
会话能力。唯一 issuer 是
`https://keycloak.localhost:8443/realms/ai-etl-demo`，Web 入口是
`https://ai-etl.localhost:3443`。该运行时仅允许 `ENVIRONMENT=dev`、
`IDENTITY_POLICY_PROFILE=personal-demo-v1`，不连接企业 tenant，也不代表 staging 或
production 已启用。

## 前提与快速验收

需要 Docker Compose、OpenSSL、curl、Python 3 和 `base32`。执行：

```bash
bash scripts/identity-demo-acceptance.sh
```

脚本创建独立 Compose project `ai-etl-identity-demo`，生成本地 CA、TLS 证书和随机
secret，启动完整栈，经公共 SCIM 接口预配虚构用户，然后完成真实密码 + TOTP 登录。
验收还要求 Web 获得 `ps1_` Cookie、会话列表显示 federated，并验证本地撤销和
RP-initiated logout。成功或失败后默认删除容器、volume 和生成资产。

保留运行时以便人工查看：

```bash
IDENTITY_DEMO_KEEP_RUNTIME=1 bash scripts/identity-demo-acceptance.sh
```

浏览器访问前需把 `secrets/dev/identity-demo/ca.pem` 临时加入本地浏览器信任库。用户名为
`demo.reader`；密码和 TOTP seed 分别位于同目录的 `demo-password` 与 `totp-secret`。
这些文件均为 ignored、权限 `0600` 的本机资产，不得提交、复制到企业环境或写入日志。

## 安全模型

Realm 使用 confidential client、authorization code + PKCE、RS256/JWKS 和官方
Keycloak step-up flow。LoA 2 流实际执行 password 与 OTP；Keycloak 的 ACR、AMR 和
authentication-time mapper 从认证执行与服务端 session note 产生声明，不使用硬编码
token claims。Query API 通过挂载的本地 CA 验证 TLS，不允许 insecure skip-verify。
虚构用户只能经 `/scim/v2/Users` provision，禁止直接修改 PostgreSQL。

## 清理与故障排查

手工清理可重复执行：

```bash
bash scripts/identity-demo-clean.sh
```

查看保留运行时的状态和日志：

```bash
docker compose -p ai-etl-identity-demo \
  -f docker-compose.yml -f docker-compose.eval.yml \
  -f docker-compose.identity-demo.yml ps
```

若证书或 realm 发生变化，先运行清理再重新验收；Keycloak import 对已有 realm 使用
`IGNORE_EXISTING`，不能用重启代替重建 volume。

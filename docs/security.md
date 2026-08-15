# Security Notes

This document records the security posture of the platform: what is hardened and
what is deliberately deferred. It is not an exhaustive threat model.

## What is hardened

- Upload size is capped.
- Multipart memory is limited and spills to disk.
- JWT is required for upload/query paths.
- Parser internal token is required outside dev.
- Production validation rejects weak defaults and wildcard CORS.
- Secrets support `KEY_FILE`/Docker secrets.

## What is intentionally not added

- Full enterprise IAM.
- Service mesh.
- Kubernetes-native policy stack.
- Centralized compliance tooling.

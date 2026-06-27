# Security Notes

This project is upgraded only as far as needed for an AI application engineer portfolio.

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

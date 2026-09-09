import type { KnowledgeSpace } from "@/lib/types";

export const EMPTY_QUERYABLE_SPACES_MESSAGE =
  "当前没有可检索的知识空间，无法问答。请联系管理员授予空间访问权限。";

export function queryableKnowledgeSpaces(items: KnowledgeSpace[]): KnowledgeSpace[] {
  return items.filter((space) => space.active);
}

export function preferredKnowledgeSpace(spaces: KnowledgeSpace[]): KnowledgeSpace | undefined {
  return (
    spaces.find((space) => space.is_default && space.kind === "production") ||
    spaces.find((space) => space.kind === "production") ||
    spaces[0]
  );
}

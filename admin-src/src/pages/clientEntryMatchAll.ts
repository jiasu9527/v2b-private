// Empty conditions mean match-all; an explicit empty email list is still one
// condition (deny-all). Existing default rules remain editable without migration.
export function requiresMatchAllConfirmation(isNew: boolean, before: readonly unknown[], after: readonly unknown[]) {
  return after.length === 0 && (isNew || before.length > 0);
}

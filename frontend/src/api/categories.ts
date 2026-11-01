import { apiGet, apiSend } from './client'

// Everything in the API refers to a category by id (UUID); the name is a
// unique label.
export interface Category {
  id: string
  name: string
  is_mandatory: boolean
  // Null for a top-level category. Only one level of nesting exists, so a
  // parent is always itself top-level.
  parent_id: string | null
}

// Fixed ids of mandatory categories the frontend has to recognize — same
// values as the backend's internal/categoryid.
export const MEMBERSHIP_FEE_ID = '00000000-0000-4000-8000-000000000001'

export function fetchCategories(): Promise<Category[]> {
  return apiGet<Category[]>('/api/categories')
}

export function createCategory(name: string, parentId: string | null): Promise<Category> {
  return apiSend<Category>('POST', '/api/categories', { name, parent_id: parentId })
}

export function deleteCategory(id: string): Promise<void> {
  return apiSend<void>('DELETE', `/api/categories/${encodeURIComponent(id)}`)
}

// Top-level categories in API order, each immediately followed by its
// sub-categories, so a list or dropdown reads as a one-level tree.
export function withChildrenUnderParents(categories: Category[]): Category[] {
  const topLevel = categories.filter((c) => c.parent_id === null)
  const ordered = topLevel.flatMap((parent) => [
    parent,
    ...categories.filter((c) => c.parent_id === parent.id),
  ])
  // A child whose parent is missing from the list shouldn't happen (FK), but
  // never drop a row silently.
  const orphans = categories.filter((c) => !ordered.includes(c))
  return [...ordered, ...orphans]
}

// Dropdown label for a category in withChildrenUnderParents order — a
// sub-category gets an indent marker, since <optgroup> can't be used (its
// label isn't selectable, and a parent category must be).
export function categoryOptionLabel(category: Category): string {
  const label = categoryLabel(category.name)
  return category.parent_id === null ? label : `\u00a0\u00a0↳ ${label}`
}

// Display label for a category id, looked up in an already-fetched list —
// for places that only carry the id (rules, budget lines). Falls back to
// the id itself while the list is loading or if the id is unknown.
export function categoryLabelById(categories: Category[] | undefined, id: string): string {
  const category = categories?.find((c) => c.id === id)
  return category ? categoryLabel(category.name) : id
}

// Turns a category's name (e.g. "membership_fee") into a display label —
// purely a display nicety for dropdowns/tables, never sent back to the API.
export function categoryLabel(name: string): string {
  return name
    .split('_')
    .filter(Boolean)
    .map((word) => word[0].toUpperCase() + word.slice(1))
    .join(' ')
}

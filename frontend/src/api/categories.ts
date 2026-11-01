import { apiGet, apiSend } from './client'

export interface Category {
  name: string
  is_mandatory: boolean
  // Null for a top-level category. Only one level of nesting exists, so a
  // parent is always itself top-level.
  parent_name: string | null
}

export function fetchCategories(): Promise<Category[]> {
  return apiGet<Category[]>('/api/categories')
}

export function createCategory(name: string, parentName: string | null): Promise<Category> {
  return apiSend<Category>('POST', '/api/categories', { name, parent_name: parentName })
}

export function deleteCategory(name: string): Promise<void> {
  return apiSend<void>('DELETE', `/api/categories/${encodeURIComponent(name)}`)
}

// Top-level categories in API order, each immediately followed by its
// sub-categories, so a list or dropdown reads as a one-level tree.
export function withChildrenUnderParents(categories: Category[]): Category[] {
  const topLevel = categories.filter((c) => c.parent_name === null)
  const ordered = topLevel.flatMap((parent) => [
    parent,
    ...categories.filter((c) => c.parent_name === parent.name),
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
  return category.parent_name === null ? label : `\u00a0\u00a0↳ ${label}`
}

// Categories are stored/filtered/matched by their raw name (e.g.
// "membership_fee") — this is purely a display nicety for dropdowns/tables,
// never sent back to the API.
export function categoryLabel(name: string): string {
  return name
    .split('_')
    .filter(Boolean)
    .map((word) => word[0].toUpperCase() + word.slice(1))
    .join(' ')
}

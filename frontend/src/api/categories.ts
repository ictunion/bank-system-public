import { apiGet, apiSend } from './client'

export interface Category {
  name: string
  is_mandatory: boolean
}

export function fetchCategories(): Promise<Category[]> {
  return apiGet<Category[]>('/api/categories')
}

export function createCategory(name: string): Promise<Category> {
  return apiSend<Category>('POST', '/api/categories', { name })
}

export function deleteCategory(name: string): Promise<void> {
  return apiSend<void>('DELETE', `/api/categories/${encodeURIComponent(name)}`)
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

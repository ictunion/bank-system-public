import { type CSSProperties, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { errMessage } from './api/client'
import { type Category, createCategory, deleteCategory, fetchCategories } from './api/categories'
import { Overlay } from './Overlay'

const COLUMNS = ['Name', 'Type', ''] as const

// Top-level categories in API order, each immediately followed by its
// sub-categories, so the table reads as a one-level tree.
function withChildrenUnderParents(categories: Category[]): Category[] {
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

export function CategoriesPage() {
  const [adding, setAdding] = useState(false)

  const { data, isPending, isError, error } = useQuery({
    queryKey: ['categories'],
    queryFn: fetchCategories,
  })

  return (
    <section>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
        <h2 style={{ margin: 0 }}>Categories</h2>
        <button onClick={() => setAdding(true)}>Add category</button>
      </div>

      {isPending ? (
        <p>Loading categories…</p>
      ) : isError ? (
        <p>Failed to load: {errMessage(error)}</p>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.875rem' }}>
            <thead>
              <tr>
                {COLUMNS.map((h, i) => (
                  <th key={i} style={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {withChildrenUnderParents(data).map((c) => (
                <CategoryRow key={c.name} category={c} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {adding && data && <AddCategoryDialog categories={data} onClose={() => setAdding(false)} />}
    </section>
  )
}

function CategoryRow({ category }: { category: Category }) {
  const qc = useQueryClient()
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const remove = useMutation({
    mutationFn: () => deleteCategory(category.name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['categories'] }),
    onError: (err) => setDeleteError(errMessage(err)),
  })

  return (
    <tr>
      <td style={category.parent_name === null ? td : { ...td, paddingLeft: '2rem' }}>
        {category.parent_name !== null && <span style={{ color: '#999' }}>↳ </span>}
        {category.name}
      </td>
      <td style={td}>{category.is_mandatory ? 'Mandatory' : 'Custom'}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>
        {category.is_mandatory ? (
          <span style={{ color: '#999' }}>Cannot be deleted</span>
        ) : (
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
            <button
              disabled={remove.isPending}
              onClick={() => {
                setDeleteError(null)
                if (window.confirm(`Delete category "${category.name}"?`)) {
                  remove.mutate()
                }
              }}
            >
              {remove.isPending ? 'Deleting…' : 'Delete'}
            </button>
            {deleteError && <span style={{ color: '#b00' }}>{deleteError}</span>}
          </div>
        )}
      </td>
    </tr>
  )
}

function AddCategoryDialog({ categories, onClose }: { categories: Category[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [parentName, setParentName] = useState('')

  // Only one level of nesting — a sub-category can't be a parent.
  const parentOptions = categories.filter((c) => c.parent_name === null)

  const create = useMutation({
    mutationFn: () => createCategory(name.trim(), parentName === '' ? null : parentName),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['categories'] })
      onClose()
    },
  })

  const canSave = name.trim() !== '' && !create.isPending

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>Add category</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (canSave) create.mutate()
        }}
      >
        <label style={field}>
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </label>

        <label style={field}>
          Parent category (optional)
          <select value={parentName} onChange={(e) => setParentName(e.target.value)}>
            <option value="">— none (top-level) —</option>
            {parentOptions.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
              </option>
            ))}
          </select>
        </label>

        {create.error && <p style={{ color: '#b00' }}>{errMessage(create.error)}</p>}

        <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem' }}>
          <button type="submit" disabled={!canSave}>
            {create.isPending ? 'Saving…' : 'Save'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Overlay>
  )
}

const field: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '0.25rem',
  marginBottom: '0.75rem',
  maxWidth: 320,
}

const th: CSSProperties = {
  textAlign: 'left',
  borderBottom: '2px solid #ccc',
  padding: '0.4rem 0.6rem',
  whiteSpace: 'nowrap',
}

const td: CSSProperties = {
  borderBottom: '1px solid #eee',
  padding: '0.35rem 0.6rem',
  whiteSpace: 'nowrap',
}
